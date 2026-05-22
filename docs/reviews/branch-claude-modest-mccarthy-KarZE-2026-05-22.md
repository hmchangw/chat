# Branch Review — `claude/modest-mccarthy-KarZE`

**Date:** 2026-05-22
**Base:** `origin/main`
**Mode:** spec-review (no Go/pkg code changes; only a new spec document at `docs/specs/message-reactions-table.md`)
**Reviewed artifact:** `docs/specs/message-reactions-table.md`

## Executive summary

This branch adds a single spec document proposing the extraction of message reactions from an embedded `MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>` column on four message tables into a dedicated side table `message_reactions` keyed by `((message_id), emoji)`. Hydration moves to read-time in `history-service` via errgroup parallel single-partition queries. Writes are out of scope (deferred to a future addReaction PR).

The `branch_review` skill's standard precondition (require Go/pkg code changes) was bypassed because the user explicitly requested a spec review. Five expert lenses ran in parallel against the spec text and supporting context (CLAUDE.md, existing schema, existing cassrepo code, existing client-api doc).

### Findings by severity

| Severity | Cassandra | Go | Conventions | Tests | Scope/Risk | **Total** |
|---|---|---|---|---|---|---|
| critical | 2 | 3 | 1 | 2 | 1 | **9** |
| high | 3 | 4 | 4 | 4 | 3 | **18** |
| medium | 4 | 4 | 4 | 3 | 4 | **19** |
| low | 3 | 3 | 5 | 2 | 3 | **16** |
| nitpick | 3 | 3 | 3 | 2 | 3 | **14** |

### Top-line risk assessment

The spec is **directionally correct but ships with one ship-blocker** and several "must resolve before the implementer starts coding" items:

- **Ship-blocker:** the spec's stated motivation in §14 (tombstone elimination by moving off the embedded `MAP`) is partly wrong — the side table relocates the tombstone problem but does not eliminate it. The motivation paragraph must be rewritten, or the design pivoted to row-per-reactor (`PRIMARY KEY ((message_id), emoji, user_id)`), which the spec doesn't evaluate.
- **High-priority decisions deferred but blocking:** compaction strategy (LCS recommended), empty-set delete race, federation note, and the SET-must-be-unfrozen confirmation. All four are schema-shape decisions that the addReaction PR cannot defer.
- **Process gap:** spec claims no `docs/client-api.md` update is needed because the wire shape is unchanged, but CLAUDE.md §5 is a hard rule with no "wire shape unchanged" carve-out. Either update the doc with a one-line clarification or get an explicit exemption written into CLAUDE.md.
- **Test plan gaps:** missing `-race` parallelism assertions, ctx-cancellation test, and explicit `testutil.CassandraKeyspace` contract for the new integration test file.

The implementation itself is small (~1–2 days for one engineer) once the spec is corrected. None of the findings argue against the side-table direction — they argue for tightening the spec text before implementation begins.

---

## Cassandra / Data-Modeling Lens

### critical

**C1. The "tombstone elimination" motivation in §14 is incorrect.** §2 declares `users SET<FROZEN<"Participant">>` — a non-frozen outer set of frozen UDTs — which supports incremental `+`/`-` updates correctly. But §14 line 181 justifies moving off the embedded shape by claiming the embedded `SET<FROZEN<UDT>>` "accumulates tombstones on every reaction remove." The actual tombstone problem on the embedded shape comes from the *outer* `FROZEN<SET<...>>` *inside* the `MAP` value (frozen → full overwrite → range tombstone on every change), not from the inner set. The side table improves locality and avoids fan-out writes across 4 tables, but does **not** fundamentally eliminate per-element-remove tombstones if `users` is non-frozen. Rewrite §14 to claim what's actually true: "tombstones isolated to a small partition that's only read on demand," not "tombstones eliminated."

**C2. Empty-set delete race (§11 line 162) requires a decision now, not in the addReaction PR.** Two concurrent removes can both read non-empty → both `set - {p}` → both observe empty → both `DELETE`. The `DELETE`s themselves are idempotent. The real hazard: a concurrent ADD interleaved between the empty-check and the DELETE will be silently erased (DELETE with no timestamp guard wins by write time). The spec hand-waves the choice (CAS, accept tombstone, or follow-up DELETE) but this choice **constrains the schema** — LWT requires `IF` semantics on the same partition and has well-known performance cost. Pin the decision in the spec.

### high

**H1. Hot-message `users` SET partition-size assumption is too optimistic.** §2 line 24 asserts "tiny partitions (a few emojis × a few participants each)." A company-all-hands message can collect thousands of reactors on a single emoji. Cassandra's collection soft-limit is 64K elements; practical pain begins in the low thousands — every read deserialises the full set, and `SET` removes generate add-tombstone-for-removed-element. **Strongly recommend evaluating the alternative the spec doesn't mention:** `PRIMARY KEY ((message_id), emoji, user_id)` — row-per-reactor. This eliminates the SET entirely, makes remove a single-row tombstone with predictable GC, supports paging reactors for very popular emojis, and naturally handles the empty-cleanup race (no row = no reaction). §14 should explicitly evaluate and reject this design, not omit it.

**H2. Federation under-specified.** §11 line 164 punts federation to a later PR, but the schema choice locks it in *now*. `((message_id))` has no `site_id` — fine if message IDs are globally unique (CLAUDE.md §6 says they are via `idgen`), but cross-site reaction state still requires OUTBOX/INBOX replication. Spec must minimally note: (a) reaction writes MUST be replicated per-site (each site's Cassandra is independent), (b) `message_id` cross-site uniqueness is the load-bearing invariant, (c) the contract if a reaction arrives via INBOX before the local message row exists.

**H3. Compaction strategy unspecified.** Default STCS is wrong for high-churn small-partition workloads with frequent removes. **LCS** is correct: bounded read amplification (1–2 SSTables per token-routed lookup), better tombstone purging cadence, fits "many small partitions, point reads." Add `WITH compaction = {'class': 'LeveledCompactionStrategy'}` to §2.

### medium

**M1. No `CLUSTERING ORDER BY` declared.** §2 omits it. Client-side sorting works, but explicit `WITH CLUSTERING ORDER BY (emoji ASC)` documents intent and prevents accidental future divergence.

**M2. Per-page fan-out cap of 50 (§7.1 line 92) needs justification.** gocql's default `NumConns=2` per host and ~32K stream-IDs per connection makes 50 trivially safe — but `LoadSurroundingMessages` already fans out, so combining doubles in-flight count. Document the cap as per-call (not global) and reuse the service's existing semaphore convention if one exists. The asserted "~5ms p99" (§14 line 180) should be flagged as needing benchmark substantiation.

**M3. TTL inheritance unspecified.** Messages may have TTLs (ephemeral rooms, retention policies). Reactions on a TTL'd message become orphan rows. Pick one: (a) apply the same TTL at reaction-write time (requires reading message TTL at write site), or (b) accept orphans and rely on a sweep job. Silently leaving orphan partitions is a slow leak.

**M4. `LoadReactionsForMessages` error semantics (§7.3 line 110).** "Fail the request" on reactions error means a transient blip on the reactions table breaks message read entirely. Reactions are auxiliary UI metadata — degraded mode (return messages without reactions, log+metric) is a defensible alternative. Surface this as a deliberate choice, not a one-liner.

### low

**L1.** `MessageReactionRow.Users []Participant` (§3 line 36) — gocql scans a CQL `SET` into a Go slice without guaranteeing order. Tests must not assert slice order; mention in §8.

**L2.** Side table deliberately drops `created_at` from the partition key (no time-range queries on reactions) — correct simplification, but worth noting in §5 docs.

**L3.** §9 DDL parity fix should also audit any inline `CREATE TABLE` strings in `history-service` integration tests, not only `message-worker`.

### nitpick

**N1.** §4 `chat.` keyspace prefix is consistent with the existing DDL files. Confirmed.

**N2.** §6 wire shape `{ "<emoji>": [Participant, ...] }` has non-deterministic key order (Go map iteration); clients presumably already tolerate this. Worth one line.

**N3.** §14 line 180 "~5ms p99" cites no benchmark. Drop the number or substantiate.

---

## Go-Code Lens

### critical

**C1. Return-type leak — `LoadReactionsForMessages` signature exposes raw `cassandra.Participant`.** §7.1 declares `map[string]map[string][]Participant` without specifying the import. Every existing repo method in `history-service/internal/cassrepo` returns `models.Message` (alias to `cassandra.Message`), never bare `cassandra.Participant`. Today `pkg/model.Message` already pulls `cassandra.Participant` transitively (`pkg/model/message.go:15`), but a method that *returns* `cassandra.Participant` directly is new surface area. The spec must either explicitly state `cassandra.Participant` or define a named alias in `cassrepo` (`type ReactionMap = map[string][]cassandra.Participant`) and use it consistently — matches the existing `type Message = cassandra.Message` alias pattern in `internal/models/message.go:5`.

**C2. Removing the `cql:"reactions"` tag from `Message.Reactions` — spec misses the consequence for existing tests.** `pkg/model/cassandra/message_test.go:185,212` round-trip the field via gocql UDT/bson marshalers. After tag removal the field still round-trips via `json`/`bson`, so behavior is preserved, but the spec should explicitly direct an update to these tests rather than only "add roundtrip test cases" (§3).

**C3. `MessageReactionRow` should NOT have a `bson` tag (CLAUDE.md §3 carve-out).** CLAUDE.md says "All model structs get both `json` and `bson` tags" — but every existing type in `pkg/model/cassandra/` (`Participant`, `File`, `Card`, …) carries `json` + `cql` only, no `bson`. The spec is correct by precedent; **call it out explicitly** to forestall reviewer churn. Also: spec is missing `json` tags on `MessageReactionRow` — add them mirroring `Participant` at `pkg/model/cassandra/message.go:12-20`. Suggested form:
```go
MessageID string `json:"messageId" cql:"message_id"`
Emoji     string `json:"emoji"     cql:"emoji"`
Users     []Participant `json:"users" cql:"users"`
```

### high

**H1. `structScan` silently tolerates untagged fields — verified, but the spec doesn't cite this.** `cassrepo/utils.go:120-123` skips fields where `tag == ""`, so dropping the `cql` tag from `Message.Reactions` is safe with the existing scan helper. Add a one-liner to §3: *"safe because `structScan` (cassrepo/utils.go:120) ignores fields without a `cql` tag."* Without the cite a reviewer can't tell whether dropping the tag will break scans of `messages_by_room` rows.

**H2. No existing semaphore convention in `history-service` to reuse.** Verified: only two errgroup sites in the service (`messages.go:72,229`), both fan-out-2 (no semaphore). The CLAUDE.md JetStream-worker semaphore pattern targets workers, not read-path fan-out. Spec's "reuse if present; otherwise 50" is fine, but the cap should be **configurable via env var** (e.g., `REACTIONS_FETCH_CONCURRENCY`, default 50) per CLAUDE.md §6 "Configuration." Hard-coded 50 will be regretted when page size tops 100.

**H3. Method naming breaks `Get…` convention.** Every read method on `*Repository` uses `Get`: `GetMessagesBefore`, `GetMessageByID`, `GetMessagesByIDs`, `GetThreadMessages` (`service.go:18-26`). Switching to `Load` breaks the convention and suggests in-place mutation. Rename:
- `LoadReactionsForMessages` → `GetReactionsByMessageIDs`
- `LoadReactionsForMessage` → `GetReactionsByMessageID`

This matches `GetMessagesByIDs` / `GetMessageByID` exactly.

**H4. Mockgen is required (not conditional) — `MessageReader` will be extended.** `service.go:16` declares `//go:generate mockgen ... MessageReader,...`. The `MessageReader` interface (`service.go:18-26`) is what handlers consume. The new repo methods belong on `MessageReader` (read-only, co-located with message reads). Spec §10's "if the handler's store interface is extended" is misleading — it **will** be extended. Strike the conditional. Also remind the implementer to commit the regenerated `internal/service/mocks/mock_repository.go`.

### medium

**M1. Hydration location — spec leaves a design choice ambiguous.** §7.3 sketches per-handler hydration in service code. Five (possibly six — see M2) call sites makes duplication a real risk. Two cleaner options:
- (a) Repo-level `GetMessagesWithReactions(ctx, page) (Page[Message], error)` — clean for callers, bloats `MessageReader`.
- (b) Service-layer helper `(s *HistoryService) hydrateReactions(ctx, []models.Message) error` — single point of error handling and metrics.

Recommend (b). Pick one in the spec; "described prose-only" invites five subtly different implementations.

**M2. Spec missed `GetThreadParentMessages` (`service.go:111`).** That handler also returns `Message`s (via `thread_parent.go:19 ParentMessages []Message`). Either it routes through `GetMessagesByIDs` (implicitly covered) or it needs its own hydration call. Confirm or list it in §7.3.

**M3. Error-wrap pattern omitted.** CLAUDE.md §3 mandates `fmt.Errorf("doing X: %w", err)`. Spec shows no examples. Existing style: `"querying messages by IDs: %w"` (`messages_by_id.go:41`). Spec should prescribe exact phrasing — e.g. `"loading reactions for message %s: %w"` — to prevent drift.

**M4. errgroup cancellation semantics not stated.** §7.1 says "fan out parallel … errgroup" but doesn't say *first error cancels siblings*. At 50-wide fan-out a slow coordinator on one shard shouldn't keep 49 goroutines running. Add: *"All goroutines receive the errgroup-derived context; first error cancels siblings."*

### low

**L1.** Empty-input godoc absent. Spec §7.1 says empty slice short-circuits, but the godoc on the spec'd signature doesn't mention it. Per Go convention and CLAUDE.md §3, the godoc must say so explicitly. Mirror `GetMessagesByIDs` godoc at `messages_by_id.go:30-33`.

**L2.** "Missing message_id returns nothing for that id" — both "no reactions" and "id never queried" surface as missing map key. Correct, but document in the godoc explicitly so handlers don't treat absence as error.

**L3.** Compile-time interface check at `service.go:115` (`var _ MessageRepository = (*cassrepo.Repository)(nil)`) — after adding methods to `MessageReader`, this will fail-fast on compile if anything is missed. Safety net exists; spec doesn't need changes.

### nitpick

**N1.** "Fail the request (do not return a partial page)" (§7.3) — agree, but specify the error matches handler conventions (`natsrouter.ErrInternal("failed to load message history")` style, `messages.go:102`).

**N2.** Spec §3 line-number reference `message.go:93` is correct. Keep this — citations matter.

**N3.** §10 should also mention re-running `make lint` after `make generate` (generated mock signatures may need import additions).

---

## Project-Conventions Lens (CLAUDE.md adherence)

### critical

**C1. Client-API doc rule violated (CLAUDE.md §5).** Spec §6 declares "no change" to `docs/client-api.md`. CLAUDE.md §5 is unambiguous: *any* PR touching a handler whose subject begins with `chat.user.{account}.request.…` MUST update `docs/client-api.md` in the same PR. The five handlers in §7.3 are documented at `client-api.md:989, 1052, 1113, 1172, 1356` and the `Message` schema entry for `reactions` lives at `client-api.md:949`. Even with identical wire shape, the read-path behaviour (error-on-hydration-failure → whole request fails, per spec §7.3 line 110) and "populated server-side via hydration" are details clients should know. Either update the doc or get an explicit exemption written into CLAUDE.md.

### high

**H1. TDD Red phase not mandated.** Spec §8 lists tests but never says "write tests first, confirm they fail, then implement." CLAUDE.md §4 calls Red-Green-Refactor mandatory with "no exceptions." Add an explicit ordering instruction to §8 and §13.

**H2. Coverage wording wrong.** Spec §8 line 135: *"Coverage target ≥90% on new code per CLAUDE.md §4."* CLAUDE.md actually mandates **≥80% floor (MUST NOT merge below)** AND **≥90% target for core business logic**. The spec conflates target with floor. Rewrite: *"≥80% floor required; ≥90% target on new repository/handler code."*

**H3. Unit-test scenarios incomplete.** Spec §8 misses: invalid/empty `messageIDs` slice argument validation, context cancellation mid-fan-out, partial errgroup failure (one of N goroutines errors — does it cancel siblings?), duplicate `messageIDs` in input, and `gocql.ErrNotFound` vs. empty-result distinction. Add these.

**H4. Federation divergence not flagged.** Spec §11 marks federation out of scope, but doesn't acknowledge the user-visible consequence: reactions written on site A will be invisible on site B even though the underlying message replicates via OUTBOX/INBOX. Add to §11: *"Until federation lands, reactions are site-local; the same `message_id` will show different reaction sets per site."*

### medium

**M1. Error-wrap pattern omitted.** CLAUDE.md §3 mandates `fmt.Errorf("doing X: %w", err)`. Spec gives signatures but no examples. Add: *"All errors wrap with `fmt.Errorf(\"loading reactions for message %s: %w\", id, err)` per CLAUDE.md §3."*

**M2. Logging discipline silent.** Spec doesn't specify whether `LoadReactionsForMessages` logs the fan-out or errors. Mandate slog-JSON structured logging with `requestID` field on errors; empty-result path stays silent.

**M3. Request-ID propagation unspecified.** Per CLAUDE.md §3 "Request Logging & Tracing." Worth stating: child goroutines inherit `ctx` (request ID survives via context), no manual re-injection needed.

**M4. `MESSAGE_BUCKET_HOURS` carve-out in CLAUDE.md not flagged.** Spec correctly drops bucketing (§14 line 180), but CLAUDE.md's Cassandra section still describes `MESSAGE_BUCKET_HOURS` as if applying to all message-related tables. Spec should add a note: "Update CLAUDE.md Cassandra section to clarify bucket math applies to `messages_by_room` / `thread_messages_by_room` only, NOT `message_reactions`."

### low

**L1.** `internal/cassrepo/` and `internal/service/` already violate CLAUDE.md §1 "flat `package main`, no `internal/`". Pre-existing; `history-service` is grandfathered. No action, worth noting in the PR description.

**L2.** Mockgen scope conditional (Spec §10 line 145). Strike "if" — `MessageReader` extension is unconditional given the new methods are called from handlers.

**L3.** `docs/specs/` is a new convention. No prior `docs/specs/` directory existed (only `docs/reviews/`, `docs/superpowers/`, …). Spec silently introduces it. Either codify in CLAUDE.md §1 or move to `docs/` flat.

**L4.** Naming OK. `message_reactions` (snake table), `MessageReactionRow` (PascalCase struct), `LoadReactionsForMessages` (verb-first method) all conform to CLAUDE.md §3 — except the verb choice (see Go lens H3).

**L5.** SSOT triple-mirror check: spec covers all three required mirrors (`docs/cassandra_message_model.md` §5, `pkg/model/cassandra/` §3, `docker-local/cassandra/init/` §4). Good.

### nitpick

**N1.** §7.1 line 92 — "cap at 50 in-flight" is a magic number. Make it a named const in the repo (and configurable per Go lens H2).

**N2.** §9 line 139 — referencing line numbers in `message-worker/integration_test.go` (50, 67, 86) will rot; describe by symbol instead.

**N3.** File naming `internal/cassrepo/message_reactions.go` is consistent with existing siblings (`messages_by_room.go`, `messages_by_id.go`, `thread_messages.go`). Fine.

---

## Test-Strategy Lens

### critical

**C1. Spec §8 fails to specify the `testutil.CassandraKeyspace` contract for the new integration test.** CLAUDE.md §4 ("Containers come from `pkg/testutil`") makes this mandatory, and every existing sibling test follows the pattern — see `history-service/internal/cassrepo/integration_test.go:17` (`testutil.CassandraKeyspace(t, "history_service_test")`). The spec must explicitly require: (a) reuse of `setupCassandra(t)` in `integration_test.go:15`, extended with the new `message_reactions` table DDL, and (b) the keyspace prefix convention (`"history_service_test"`, identical to siblings — not `t.Name()`-derived; the helper already hashes `t.Name()` internally at `pkg/testutil/cassandra.go:110`).

**C2. §7.1 under-specifies concurrency testing.** Per CLAUDE.md §4 General ("ALWAYS use the `-race` flag"), the errgroup fan-out *must* have an explicit race-detector integration test asserting actual parallelism (seed 100 messages with reactions, assert all return correctly under `-race`, and assert the in-flight cap of 50 is respected — e.g. via an injectable hook or by counting concurrent gocql sessions). The spec's "concurrent fan-out" bullet at §8 is too vague to verify the bound. **Also missing: a context-cancellation test** (cancel ctx mid-fan-out, assert `LoadReactionsForMessages` returns `ctx.Err()` and goroutines exit cleanly — required to prove no goroutine leak per CLAUDE.md §3 "Concurrency").

### high

**H1. No `TestMain` guidance for new test file.** §8 introduces `internal/cassrepo/message_reactions_integration_test.go` but doesn't note `TestMain` is already satisfied by `cassrepo/main_test.go:11` (`testutil.RunTests(m)`). Fine in practice, but the spec should say "new file lives in package `cassrepo`, reusing the existing `TestMain`" so an implementer doesn't add a duplicate `TestMain`.

**H2. Empty-input short-circuit assertion missing.** §7.3 says "Empty page → skip the call entirely", but §8 unit tests list only "empty page → reactions store not called" without specifying *how* it's asserted. Mockgen's `EXPECT().LoadReactionsForMessages(...).Times(0)` is the correct form — should be in the spec verbatim, otherwise implementers tend to write `gomock.Any()` and accidentally accept zero-length calls.

**H3. Mock regeneration step buried.** §10 says "Run `make generate SERVICE=history-service`" but doesn't enumerate which interface gets the new method. The new method `LoadReactionsForMessages` only belongs on `MessageReader` (read path) at `internal/service/service.go:18–26` — adding it to `MessageWriter` or `MessageRepository` directly would be wrong. Spec must name `MessageReader` explicitly, and remind the implementer to commit the regenerated `internal/service/mocks/mock_repository.go`.

**H4. Removing assertions risks coverage loss.** §8 says "remove embedded-reactions seed data and assertions" — but `cassrepo/messages_by_id_integration_test.go:165-169` and `thread_messages_integration_test.go:301-305` are full row-round-trip tests where reactions are one of ~20 fields verified. Pure deletion is fine *only if* the test still asserts every other column. Spec should explicitly direct: "Delete the reactions seed/assert blocks. Do not delete the surrounding row-round-trip assertions. Do not delete the test cases entirely."

### medium

**M1. No backward-compat test for empty reactions wire shape.** Per spec §6 ("wire shape stays identical"), one handler test per endpoint should assert that a message with zero reactions still serialises as `"reactions": {}` (not `null`, not omitted) after JSON marshal. Trivial, prevents a real client-API regression.

**M2. No federation/site-leak test.** Since `message_reactions` partitions by `message_id` alone (no `site_id`), at minimum add an integration test asserting `LoadReactionsForMessage("known-id")` returns the expected payload from the *local* keyspace only.

**M3. No benchmark.** The whole motivation (§14) is read-path performance. Add a `BenchmarkLoadReactionsForMessages` (e.g. 50 messages, 5 reactions each) so the next refactor has a baseline. Non-blocking, nice-to-have.

### low

**L1.** Defensive "missing message_id" covered (§8 bullet 5), but no test for nil/empty `[]string{}` slice. Add a repo-level unit: `LoadReactionsForMessages(ctx, nil)` and `(ctx, []string{})` both return `(map{}, nil)` with zero queries dispatched.

**L2.** Single message with reactions vs none — not in §8. Add to handler unit tests as a distinct table row from "happy path with reactions on some messages" — page-size-1 edge often breaks fan-out loops.

### nitpick

**N1.** §9 (message-worker DDL fix) doesn't mention the same `reactions MAP<...>` strings exist at `history-service/internal/cassrepo/integration_test.go:49,76,105` and `internal/service/integration_test.go:51,64,78` — six call sites total in history-service alone. List them so nothing's missed.

**N2.** No `testdata/` mention — fine, existing cassrepo tests don't use fixtures, no reason to start now.

---

## Scope & Risk Lens

### critical

**C1. SET frozenness must be made explicit.** §2 declares `users SET<FROZEN<"Participant">>`. §11's write path `SET users = users + {?}` only works on an **unfrozen** outer SET (incremental update of FROZEN collections is not supported in CQL). The shape as written IS unfrozen outer + frozen inner UDT, which is correct — but the embedded model used `FROZEN<SET<FROZEN<"Participant">>>` (cassandra_message_model.md:91), so a copy/paste regression would silently break the future writer. Add one line to §2: *"outer SET is intentionally UNFROZEN so incremental `+ {?}` / `- {?}` updates are supported."*

### high

**H1. Local-dev rollout will break existing dev Cassandra.** Spec claims "no production data" but ignores developer laptops. Dropping a column from 4 tables on an existing local keyspace requires `ALTER TABLE … DROP reactions` — the init `.cql` files only run on a *fresh* keyspace. Devs who pulled prior schema will have stale `reactions` columns. Rollout §13 should explicitly direct: "drop and recreate the local keyspace, or run a one-shot `ALTER TABLE … DROP reactions` migration script."

**H2. Federation user-visible divergence unflagged.** Until cross-site reaction federation lands, site A and site B will show different reaction state for the same federated message. UX consequence, not implementation detail. Add to §11 as a "Known limitation until federation lands."

**H3. No acceptance criteria.** Spec lacks an explicit definition-of-done. Add: all 5 history handlers hydrate reactions; integration tests cover sparse/empty/full pages; ≥80% coverage on new code (≥90% target); `make lint`, `make test`, `make test-integration SERVICE=history-service`, `make sast` all green; `docs/cassandra_message_model.md` updated.

### medium

**M1. Reversibility / rollback not addressed.** If reverted, the dropped `reactions` column must be re-added on 4 tables. Cheap *today* (no live writers) — but the spec should say so explicitly: *"Rollback window: until the first `addReaction` writer ships. After that, rollback requires backfill from `message_reactions` into the embedded column on 4 tables."*

**M2. Tombstone problem partially relocated, not eliminated (§14).** Cross-cuts with Cassandra-lens C1. §14 cites tombstones as the motivation, but `SET<...> - {?}` removes still generate tombstones in the side-table partition. The win is real (tombstones isolated to a small single-message partition) but the spec overstates by implying the problem is *solved*. Reword to "tombstones isolated to a small partition that's only read on demand."

**M3. Message ID uniqueness assumption.** Spec asserts message IDs are unique across the site, justifying one table for regular + thread messages. `idgen.GenerateMessageID()` produces 20-char base62 — collision-resistant, ~119 bits entropy. Worth one explicit line: *"Assumption: message IDs are globally unique by construction; federation imports do not rewrite IDs."*

**M4. p99 claim unsubstantiated (§14).** §14 line 180 says "~5ms p99 difference" — no benchmark cited. Either link a benchmark or soften to "expected single-digit ms" and add a post-rollout measurement target.

### low

**L1. Read amplification not budgeted.** Up to ~50 parallel single-partition reads per `LoadHistory` page (default page size?). Spec caps concurrency at 50 but doesn't say what happens when page size > 50, or what the per-message timeout is. Add per-message context with a short deadline, and explicit page-size assumption.

**L2. Mocks step (§10) conditional and vague.** "If the handler's store interface is extended" — given §7.3 adds new repo methods called from handlers, the interface almost certainly *is* extended. Make this unconditional. Cross-cuts with Test-lens H3, Conventions-lens L2.

**L3. Frontend coordination (§6).** Client API already documents `reactions` as Optional (`client-api.md:949`) — confirm in §6: "Clients already treat `reactions` as optional; no frontend change required."

### nitpick

**N1.** §14 "Decisions Walked Back" is useful — keep it. Saves future reviewers from re-litigating the bucketed-schema idea.

**N2.** Time estimate. Optional: a 1-line "~1–2 days for one engineer" helps scheduling.

**N3.** Cross-team handoff is fine. history-service can ship first with empty `message_reactions` — current state of the world. §13 ordering is correct.

---

## Prioritized Action List

Top items across all lenses, ordered by severity then impact ÷ effort:

| # | Sev | Action | Where | Why |
|---|---|---|---|---|
| 1 | critical | Rewrite the tombstone motivation in §14 — drop "eliminates tombstones," replace with "isolates tombstones to small on-demand partitions." | spec §14 line 181; cassandra-lens C1 + scope-lens M2 | The stated reason for the whole refactor is partly wrong; reviewers will catch it. |
| 2 | critical | Add `docs/client-api.md` update (one-line clarification on the five affected handlers + Message schema entry) OR get an explicit exemption written into CLAUDE.md §5. | spec §6; client-api.md:949,989,1052,1113,1172,1356 | CLAUDE.md §5 is a hard rule, no "wire shape unchanged" carve-out. |
| 3 | critical | Make SET frozenness explicit ("outer SET intentionally UNFROZEN"). | spec §2 line 19 | Prevents a silent copy/paste regression that would break the addReaction writer. |
| 4 | critical | Pin the empty-set cleanup strategy (CAS vs accept tombstone vs follow-up DELETE) in this spec, not the addReaction PR. | spec §11 line 162; cassandra-lens C2 | Constrains the schema (LWT requires `IF` semantics on the same partition). Cannot be deferred. |
| 5 | critical | Specify the `testutil.CassandraKeyspace` contract for the new integration test (prefix `"history_service_test"`, reuse `setupCassandra(t)`). | spec §8; test-lens C1 | Otherwise the implementer spins a custom container and breaks the shared-container pattern. |
| 6 | critical | Add explicit `-race` parallelism test AND ctx-cancellation test for the errgroup fan-out. | spec §8; test-lens C2 | CLAUDE.md §3 "Concurrency" + §4 require both. |
| 7 | high | Switch method naming from `Load*` to `Get*` (`GetReactionsByMessageIDs` / `GetReactionsByMessageID`). | spec §7.1; go-lens H3 | Matches existing repo convention (`GetMessagesByIDs`, `GetMessageByID`). |
| 8 | high | Make the concurrency cap configurable via `REACTIONS_FETCH_CONCURRENCY` env var (default 50). | spec §7.1 line 92; go-lens H2 + conventions M3 | CLAUDE.md §6 requires env-driven config; hardcoded magic numbers don't survive page-size growth. |
| 9 | high | Evaluate (and either adopt or explicitly reject in §14) the row-per-reactor alternative `PRIMARY KEY ((message_id), emoji, user_id)`. | spec §14; cassandra-lens H1 | Eliminates SET-size hot-spotting on viral messages; the spec doesn't even mention it. |
| 10 | high | Add LCS compaction (`WITH compaction = {'class': 'LeveledCompactionStrategy'}`) to the CREATE TABLE in §2. | spec §2 line 16; cassandra-lens H3 | STCS is wrong for this workload; decision can't wait for the addReaction PR. |
| 11 | high | Add a federation paragraph: "Reactions are site-local until federation lands; the same `message_id` will show different reaction sets per site." | spec §11; cassandra-lens H2 + scope-lens H2 + conventions H4 | Three lenses raised it — load-bearing UX consequence. |
| 12 | high | Fix coverage wording: "≥80% floor required; ≥90% target on new repository/handler code." | spec §8 line 135; conventions H2 | Spec conflates target with floor. |
| 13 | high | Local-dev migration note: "drop+recreate keyspace OR run `ALTER TABLE … DROP reactions` on existing dev DBs." | spec §13; scope-lens H1 | Otherwise developers will silently diverge. |





