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


