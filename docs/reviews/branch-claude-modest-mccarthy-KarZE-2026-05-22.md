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

