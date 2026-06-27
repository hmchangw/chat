# Likely Incident Scenarios

> Codebase-grounded incident-readiness audit of the distributed multi-site chat system.
> Method: 10 parallel subsystem finders swept four dimensions (data-loss, availability,
> correctness, operational); each scenario was adversarially verified against the real
> code before inclusion. 67 raw scenarios → 49 survived verification → 18 refuted.
> Likelihood/severity values are the **verifier-corrected** ratings, not the finders' originals.

## Top risks at a glance

- **MESSAGE_BUCKET_HOURS config drift is the single highest-aggregate risk** — six independent finders converged on it. Corrected verdict: a misconfiguration/deploy-discipline hazard (likelihood medium, severity critical), **not** a code bug. Both services default to 72h, so drift requires explicit operator error or a staggered rollout — but if it happens, message history silently vanishes for reads with no error surfaced.
- **Cross-site federation has two confirmed correctness/data-loss holes**: a removed-then-restricted user can end up over-privileged on their home site (access-control bypass), and a member added before their user record syncs is silently dropped — both confirmed, both medium likelihood / high severity.
- **Presence and history-service degrade silently under partial failure** — cross-site presence queries fall back to "offline" with no signal to the client (confirmed), and history-service lacks the HandlerTimeout that user-service has, so slow Cassandra reads can starve subscription slots.
- **A missing/un-provisioned INBOX stream causes a complete, silent federation outage** for the affected site — publishers see success while NATS drops the events (confirmed). Low likelihood, critical severity.
- **DEV_MODE leaking to production disables OIDC validation** — auth bypass for any account the attacker holds a keypair for (the "any account" claim is overstated; NATS JWT key-binding limits blast radius). Medium likelihood, high severity.
- **Several "data loss" reports are really transient-duplicate or recoverable scenarios** once verifier corrections are applied (gatekeeper Ack-failure dup, DM fire-and-forget, key-fanout failure recovered by client `ensureKey`). Kept but down-ranked — they are reliability/efficiency concerns, not silent loss.
- **The dominant cross-cutting theme is silent degradation**: at least a dozen findings share the same shape — an operation "succeeds" (HTTP 200, Ack, empty result set) while data is incomplete, with no metric or alert wired to catch it.

---

## 1. Data loss / corruption

### 1.1 — MESSAGE_BUCKET_HOURS config drift → silent history loss `[MERGED: 6 finders]`
**Summary:** A bucket-window mismatch between message writers and history readers sends writes and reads to different Cassandra partitions, making messages durably present but invisible to reads.
**Likelihood:** medium · **Severity:** critical · **RISK: highest in group**
*Merged from* message-hotpath, cassandra-storage, rooms-mongo, federation-inbox, platform-ops, user-history-services. Subsystems: `message-worker` (write), `history-service` (read), `pkg/msgbucket`, Cassandra `messages_by_room`.

**Trigger:** A deploy/rollback leaves `message-worker` and `history-service` (or two sites) on different `MESSAGE_BUCKET_HOURS` (e.g. 72 vs 48), or a single service is restarted with a changed value after data was already written.
**Mechanism:** Bucket key is `(t.UnixMilli() / windowMs) * windowMs`, computed independently per service (`message-worker/main.go:78`, `history-service/internal/config/config.go:46`, math in `pkg/msgbucket/bucket.go:20-21`). A write at 72h lands in one partition; a read at 48h walks a different bucket sequence (`history-service/internal/cassrepo/walker.go:119-125`) and never touches it. Cassandra returns 0 rows, not an error.
**Impact:** Range queries (`GetMessagesBefore/After/Between`) return incomplete/empty results. The thread-reply dual-write magnifies it — a reply can vanish from the channel timeline while remaining visible in `thread_messages_by_thread` (no bucket column). Data is **not** lost from Cassandra, only unreachable.
**Caveat (verifier corrections):** Likelihood is **medium, not high** — the code is correct; both services default to 72h, so this requires operator error or a staggered rollout, not automatic drift. The originally-claimed broader blast radius (broadcast-worker `room.lastMessage`, `GetThreadFollowers`, notification push) is **false** — those paths are MongoDB-backed or consume from MESSAGES_CANONICAL, not the bucketed table. Impact is scoped to **Cassandra history reads only.**
**Detection gap:** No startup cross-service config check, no metric tying write-count to read-count, no "rows found vs expected" gauge. An empty range is indistinguishable from a genuinely empty room.
**Mitigation:** Make `MESSAGE_BUCKET_HOURS` required (no default) and validate parity at startup against a shared source of truth (Cassandra schema metadata, or a config row); fail FATAL on mismatch. Emit `message_bucket_hours{service,siteID}` at startup for Prometheus drift alerting. Treat it as an immutable, ops/IaC-enforced site-wide constant; changing it requires a re-bucketing migration job.

### 1.2 — Federated member_added silently dropped when user absent from local collection `[CONFIRMED]`
**Summary:** A user added to a remote room never gets a subscription if their user document hasn't synced to the destination site yet; the event is Ack'd as success.
**Likelihood:** medium · **Severity:** high · **RISK: high**
Subsystem: `inbox-worker` (cross-site user-existence assumption).

**Trigger:** User A (homed on site-b) is added to a room on site-a; site-a publishes `member_added` to site-b, but A has no document in site-b's `users` collection yet (sync race / deletion / broken sync).
**Mechanism:** `handleMemberAdded` calls `FindUsersByAccounts`; missing accounts hit `!ok`, log `slog.Warn("user not found for account")` and `continue` (`inbox-worker/handler.go:128-135`). With zero subs built, the function returns `nil` (`:166-174`), so the message is Ack'd. No subscription is created; no retry.
**Impact:** Permanent, silent cross-site membership divergence — site-a believes A is a member; site-b has no record. A never sees the room on their home site.
**Detection gap:** Only a WARN log; no metric, no alert, no periodic member-list reconciliation across sites.
**Mitigation:** Treat missing accounts as a **retriable error** (Nak → redelivery gives sync time to catch up) instead of a silent skip. Escalate the log to ERROR and add a counter. Optionally pre-flight user existence in `room-worker` before publishing.

### 1.3 — Upload orphan: file in MinIO, metadata never persisted
**Summary:** Upload-service stores the blob but persists no metadata; if the client never references it, the file leaks forever.
**Likelihood:** medium · **Severity:** medium · **RISK: medium**
Subsystem: `upload-service` (MinIO + MongoDB).

**Trigger:** `UploadGroupImages` succeeds, but the client crashes / drops the attachment before the message-send RPC.
**Mechanism:** `HandleUploadFile` uploads and returns an `Attachment` struct but never writes metadata anywhere; the store interface has only `IsMember`/`GetRoomSiteID` (`upload-service/handler.go:245-262`, `store.go:14-23`). Referencing the file is entirely the client's responsibility.
**Impact:** Unbounded MinIO growth; eventually disk-full I/O errors block all uploads. No audit trail for orphans.
**Caveat:** Likelihood **medium, not high** — well-behaved clients use the attachment immediately; orphaning is an edge case. The "driveHost lookup fails silently" sub-trigger is not well-founded (fallback URL exists).
**Detection gap:** No upload-vs-referenced metric; MinIO doesn't report unused objects until it's full.
**Mitigation:** Persist attachment metadata on upload with a TTL index; mark "referenced" on message-send; sweep unreferenced files older than N days. Add an `upload_orphans_total` gauge.

### 1.4 — Partial multi-table soft-delete leaves message deleted in one mirror, alive in another `[MERGED: 2 finders]`
**Summary:** Soft-delete CASes `messages_by_id` then issues plain UPDATEs to mirror tables; a failure between them leaves the message deleted to one read path and alive to another.
**Likelihood:** low · **Severity:** high (medium per the cross-site variant) · **RISK: medium**
*Merged from* cassandra-storage (write path / thread cascade) and user-history-services (multi-table consistency). Subsystem: `history-service` `SoftDeleteMessage`.

**Trigger:** Transient Cassandra failure mid-cascade after the LWT on `messages_by_id` succeeds.
**Mechanism:** Only `messages_by_id` is gated by an LWT (`IF deleted != true`, `write.go:277-280`); the mirror updates to `messages_by_room`, `thread_messages_by_thread`, `pinned_messages_by_room` are unconditional sequential UPDATEs (`:310-333`). On retry the LWT now fails (`deleted` already true), so the mirrors are never re-applied — the inconsistency is permanent absent repair.
**Impact:** A message reads deleted via `messages_by_id` but alive via `messages_by_room` (the common read path). For thread replies, the channel copy can stay visible after the thread copy is gone. Interacting with the "alive" copy fails ("not found").
**Caveat:** The claimed **broadcast/cross-site propagation is impossible** — the broadcast event is published only *after* `SoftDeleteMessage` returns success, so a mirror failure aborts the publish. The calling client does get an error. Likelihood is genuinely low (requires a ~10s timeout in a precise window). This is a transient-visibility / consistency hole, not loss of the underlying row.
**Detection gap:** No cross-table consistency audit; no per-table delete-failure metric.
**Mitigation:** Batch mirror updates (LOGGED batch) or use a delete-intent row + idempotent background applier. On the read path, treat `messages_by_id` (the LWT-gated row) as the source of truth for `deleted`.

### 1.5 — Room member added but key delivery lost (E2E) `[MERGED: 2 finders, see caveat]`
**Summary:** `fanOutKey` is best-effort core-NATS publish; a partial/permanent publish failure can leave a new member subscribed but without the room key.
**Likelihood:** low–medium · **Severity:** low–high (see caveat) · **RISK: low–medium**
*Merged from* roomcrypto's "key delivery lost on add" and "fan-out partial delivery under partition." Subsystem: room E2E crypto / key distribution.

**Trigger:** Network flake during a (large) member-add; `keySender.SendData` fails for a subset of accounts.
**Mechanism:** `fanOutKey` publishes per-account via core NATS (`roomkeysender.go:44-52`) — fire-and-forget, no durability. Failures are logged and counted (`FanoutErrors`) but `wg.Done()` runs regardless and the room-worker job Acks (`room-worker/handler.go:2175-2225`). No retry for the failed subset.
**Impact:** Affected members are subscribed (Mongo) but cannot decrypt new messages.
**Caveat (important):** The "new member cannot decrypt history" framing is **overstated**. The frontend detects a missing key on decrypt and calls `ensureKey()`, which gates on the same persisted subscription and fetches the key on demand — recovery is transparent (at worst a brief `[encrypted message]` placeholder). The realistic residual risk is the **bulk partition variant** (medium/high): many accounts failing at once before any client-side recovery, with no server-side record of which failed. Down-ranked accordingly.
**Detection gap:** `FanoutErrors` spikes but no per-account tracking and no retry; decryption failures are client-side and not reported back.
**Mitigation:** Move key delivery to JetStream with per-account durable consumers; Ack only after confirmed publish. Keep the client-side `ensureKey` sync-RPC fallback as defense-in-depth.

### 1.6 — Gatekeeper duplicate on Ack-failure after CANONICAL publish `[DOWN-RANKED]`
**Summary:** If the MESSAGES Ack fails after CANONICAL publish + client reply both succeed, redelivery can produce a duplicate CANONICAL event once the dedup window expires.
**Likelihood:** low · **Severity:** medium · **RISK: low (down-ranked)**
Subsystem: `message-gatekeeper`.

**Caveat:** The finder's headline ("reply before ensuring CANONICAL success") is **incorrect** — `sendReply` runs only after `processMessage` returns nil, which requires CANONICAL publish success (`handler.go:376` → `:142` → Ack `:144`). The real residual is the narrow Ack-failure-after-everything-else-succeeded window producing a **duplicate**, not loss — and dedup absorbs it within the window. Downstream upserts are idempotent; the visible effect is at most a momentary duplicate in the UI event stream. Mechanically correct, practically improbable.
**Detection gap:** Ack failure is logged (`:145`) but no counter; no redelivery metric.
**Mitigation:** Retry `msg.Ack` with backoff before treating the message as confirmed; emit an Ack-failure counter.

---

## 2. Availability / outages

### 2.1 — INBOX stream missing at destination → silent total federation outage `[CONFIRMED]`
**Summary:** If a remote site's INBOX stream isn't provisioned, cross-site publishes are dropped by NATS with no error to the publisher.
**Likelihood:** low · **Severity:** critical · **RISK: highest in group**
Subsystem: cross-site federation / stream bootstrap.

**Trigger:** ops/IaC fails to pre-create `INBOX_<remoteSite>`, or it's deleted.
**Mechanism:** Publishers JetStream-publish to `chat.inbox.{destSiteID}.external.…` (`room-worker/handler.go:1186`); the publish reaches NATS and "succeeds," but with no matching stream at the destination the message is silently dropped. `inbox-worker`'s existence check is **local-only at its own startup** (`inbox-worker/bootstrap.go:45-63`) — it never validates remote streams.
**Impact:** Complete, silent federation outage for the affected site — all member/role/read-state/thread events dropped. Events published during the window are permanently lost.
**Detection gap:** No remote-stream health check from publishers; idle consumer lag looks normal; only a slow membership-divergence audit would catch it.
**Mitigation:** A bootstrapper that verifies each remote `INBOX` exists on startup/periodically; a federation health-check ping confirming the remote consumer processes; or explicit Sources+SubjectTransform sourcing so NATS enforces stream existence.

### 2.2 — history-service has no HandlerTimeout → slow Cassandra reads starve request slots
**Summary:** Unlike user-service, history-service registers no HandlerTimeout, so a slow bucket-walk can occupy a subscription slot far longer than expected.
**Likelihood:** medium · **Severity:** high · **RISK: high**
Subsystem: `history-service` message pagination.

**Trigger:** Large bucket walk over slow/partitioned Cassandra.
**Mechanism:** Middleware chain is Recovery → RequestID → Logging with **no** `HandlerTimeout` (`history-service/cmd/main.go:193-198`), whereas user-service applies `HandlerTimeout(15s)` (`user-service/main.go:96`). A read can walk up to `MESSAGE_READ_MAX_BUCKETS` (122) buckets; the handler context has no deadline, so it relies entirely on per-query timeouts.
**Impact:** Slow requests occupy worker/subscription slots; under load they pile up and starve siblings; clients hit the global NATS timeout and retry, amplifying the stall.
**Caveat:** Likelihood **medium, not high**, and severity **high, not critical** — individual gocql queries **do** time out at 10s (`cassutil/cass.go:61`), so true indefinite hangs don't occur; the impact is very-slow requests and slot starvation, not an infinite wait. Triggering needs slow Cassandra *and* a wide sparse-bucket walk.
**Detection gap:** No error reply on slow reads (handler never returns within budget); growing goroutine count and latency percentiles only.
**Mitigation:** Add `HandlerTimeout` (~15s) to history-service and propagate the deadline into gocql queries via context.

### 2.3 — Cross-site presence query degrades to "offline" on peer timeout `[CONFIRMED]`
**Summary:** When a home site is unreachable, the querying site silently returns its users as offline with a 200 OK.
**Likelihood:** medium · **Severity:** high · **RISK: high**
Subsystem: `user-presence-service` cross-site queries.

**Trigger:** Peer site unreachable / GC pause; the 3s `PEER_TIMEOUT` expires.
**Mechanism:** `QueryBatch` fans out per remote site; a peer error logs ERROR but returns `nil` (`handler.go:209-222`), so affected accounts simply don't appear in `statusByAccount` and default to `StatusOffline` at assembly (`:228-242`).
**Impact:** Silent cross-site divergence — a user shows offline on one site, online on their home site. Breaks typing indicators / "available agents" and violates the location-transparency invariant.
**Caveat:** Confirmed and **intentional** per code comments; the response model carries no degradation metadata, so clients genuinely cannot distinguish timeout-offline from real-offline.
**Detection gap:** No divergence detection; peer failures logged but not alerted.
**Mitigation:** Add a `confident` flag to the response (false when any peer failed); optionally return last-known status instead of offline; alert on peer-query failure rate.

### 2.4 — CCS silent index unavailability → searches silently miss remote messages
**Summary:** With `ignore_unavailable=true`, an unreachable remote ES cluster returns 200 with empty hits, so cross-cluster search silently degrades to local-only.
**Likelihood:** medium · **Severity:** high · **RISK: medium-high**
Subsystem: `search-service` / `searchengine` adapter.

**Trigger:** Remote ES cluster down / network partition / broken `*:messages-*` CCS route.
**Mechanism:** Index pattern hardcodes `*:messages-*` (`query_messages.go:18`); adapter passes `ignore_unavailable=true&allow_no_indices=true` (`adapter.go:212-217`); the handler parses zero hits and returns `Total=0` (`handler.go:110-114`) — indistinguishable from "no matches."
**Caveat:** Documented design choice; **local** messages (`messages-*` first in the pattern) are always present, so this is partial cross-cluster degradation, not total search loss. Severity high, not critical.
**Detection gap:** ES returns 200; the `_clusters`/`shards_successful` envelope that would reveal it isn't inspected; no per-cluster search metric.
**Mitigation:** Inspect the `_clusters` block and log/alert on skipped clusters; or move to app-level fan-out so remote unavailability is explicit.

### 2.5 — JWKS stale-cache trusts a revoked key (auth)
**Summary:** If an issuer revokes a key but it lingers in go-oidc's cache and a token is signed with it, that token may still validate.
**Likelihood:** low · **Severity:** medium · **RISK: low-medium**
Subsystem: `pkg/oidc` JWKS caching.

**Caveat:** The "new tokens rejected after rotation" half is **refuted** — go-oidc auto-fetches on unknown `kid` (`jwks.go:169-176`). Only the "revoked key still trusted" half survives, and it's conditional: the revoked key must still be cached *and* most issuers remove revoked keys from the JWKS endpoint immediately. Attacker also needs the actual key material.
**Detection gap:** No metrics on JWKS refresh / kid changes.
**Mitigation:** Monitor the issuer's `jwks_uri` for kid changes and alert on rotation; configure an explicit cache TTL; readiness check on JWKS reachability.

### 2.6 — Clock skew prematurely rejects valid JWTs (auth)
**Summary:** No clock-skew margin when minting NATS JWTs; large unsynced drift could expire tokens immediately or trip OIDC nbf/exp.
**Likelihood:** low · **Severity:** high (if it occurs) · **RISK: low-medium**
Subsystem: `auth-service` + NATS.

**Caveat:** Down-ranked from medium — go-oidc applies ~1min leeway by default and NATS runs NTP-synced in production; the 2h JWT lifetime gives ample buffer. Real but largely mitigated by library defaults and infra norms.
**Mechanism/evidence:** `jwtExpiryAt()` uses `time.Now()` with no margin (`handler.go:262-264`).
**Detection gap:** No "JWT rejected due to expiry/time" metric.
**Mitigation:** Add a small mint-time buffer; configure explicit OIDC leeway; enforce NTP/chrony; add a time-drift health check vs issuer/NATS.

### 2.7 — Graceful-shutdown timeout → duplicate reprocessing (not loss) on pod termination `[DOWN-RANKED]`
**Summary:** A slow handler that doesn't Ack within the 25s shutdown window leaves its message unacked; JetStream redelivers it to a new pod.
**Likelihood:** medium · **Severity:** medium · **RISK: medium (down-ranked)**
Subsystem: `pkg/shutdown` + JetStream pull loops (broadcast-worker, message-worker, inbox-worker).

**Caveat:** The "silent data loss" framing is **wrong** — messages are persisted to Cassandra *before* Ack, and redelivery re-inserts idempotently. The real impact is **duplicate processing** (wasted work, possibly duplicate notifications), and the system self-heals. Likelihood medium (depends on actual handler latencies, not the assumed >15s).
**Evidence:** `iter.Stop()` → `wg.Wait(ctx, 25s)` then `nc.Drain()` (`broadcast-worker/main.go:243-255`, `message-worker/main.go:241-254`); fixed 25s (`shutdown.go:15`).
**Detection gap:** Silent context timeout; a Redelivered-counter spike ~AckWait later is the only signal.
**Mitigation:** App-level store-operation timeouts so slow queries cancel rather than hang; size `MAX_WORKERS` to handler latency; a post-`wg.Wait` poison-Ack hook; latency histograms with a p99-near-grace alert.

### 2.8 — inbox-worker membership-lane loses buffered message on shutdown
**Summary:** On shutdown the membership channel is closed unconditionally; a message buffered (not yet processing) is lost because it never enters `process()`/Ack.
**Likelihood:** medium · **Severity:** high · **RISK: medium**
Subsystem: `inbox-worker` 2-lane pattern.

**Trigger:** Shutdown while a membership message sits buffered in `membershipCh` (arrival rate > lane throughput, e.g. slow Mongo upsert).
**Mechanism:** Feeder `defer close(membershipCh)` (`main.go:546-568`); the lane goroutine range-loops over it (`:539-544`). A buffered-but-unprocessed message is dropped on close regardless of Ack. The main sequence proceeds to `nc.Drain()` after `wg.Wait(25s)` (`:580-599`).
**Caveat:** The verifier reframes the root cause — it's the **unconditional close without draining buffered messages**, structural, not primarily an Ack-timeout race. In-flight (actively-processing) messages are handled differently (late `wg.Done`, background goroutine). JetStream redelivery (~AckWait) recovers unless no pod replaces it.
**Detection gap:** "drain timed out" near 25s; no Ack/Nak for the dropped message; later redelivery; cross-site subscription counts diverge.
**Mitigation:** Drain `membershipCh` synchronously before closing; add a handler-level Mongo timeout so writes fail fast; poison-drop hook after `wg.Wait`.

### 2.9 — search-sync-worker panic in `jobguard.Guard` → poison redelivery loop `[DOWN-RANKED]`
**Summary:** `Guard`'s return value is ignored, so a panicking message stays unacked and redelivers until MaxDeliver.
**Likelihood:** low · **Severity:** low · **RISK: low (down-ranked)**
Subsystem: `search-sync-worker`.

**Caveat:** Largely refuted — the un-acked-on-panic behavior is **intentional** (documented in `jobguard.go`), no realistic nil-deref path exists (event structs are value types), validation errors Ack immediately (`handler.go:70`), and redelivery is **bounded** by MaxDeliver=5 with backoff, not infinite. Keep only as a defensive-hardening note.
**Mitigation:** Check `Guard`'s return and Nak/poison-drop explicitly; add nil guards in `BuildAction`.

---

## 3. Correctness / consistency

### 3.1 — Cross-site restrict applied before member sync → over-privileged remote user `[CONFIRMED]`
**Summary:** A room restriction processed on a remote site before the user's subscription exists never demotes them; a later `member_added` sets `RoleMember`, bypassing the restriction.
**Likelihood:** medium · **Severity:** high · **RISK: highest in group**
Subsystem: `inbox-worker` / cross-site visibility updates.

**Trigger:** Admin restricts a room on site-A; the restrict event reaches site-B before the user's subscription is created there.
**Mechanism:** `ApplySubscriptionVisibility` is best-effort and no-ops on zero matched subs (`inbox-worker/handler.go:61`; `UpdateMany` doesn't warn on matched=0, `room-service/store_mongo.go:1595`). Later `handleMemberAdded` uses `rolesForType` and does **not** re-read post-restrict room state (`handler.go:157`), so the new sub gets `RoleMember`.
**Impact:** Owner (site-A) sees the room restricted; the federated user (site-B) sees themselves with full member access — a genuine cross-site **access-control bypass**.
**Detection gap:** No cross-site `(room.restricted, subscription.roles)` audit; audit logs don't span sites.
**Mitigation:** `handleMemberAdded` must apply current room restrict state to newly-created subscriptions; or order visibility-before-member-add; or make role writes timestamp-idempotent so a later visibility event overrides.

### 3.2 — notification-worker stale roomsubcache → removed member receives push (info leak)
**Summary:** A member removed just before a message is published can still pass the cached-membership gate and receive a notification.
**Likelihood:** medium · **Severity:** high · **RISK: high**
Subsystem: `notification-worker` / `roomsubcache`.

**Trigger:** Member removal and a message publish arrive close together while the cache entry is still populated.
**Mechanism:** Invalidation happens on member-change events (`handler.go:86-90`) but the message path reads cached members (`:99`) and filters only muted/restricted (`:135-150`), not live subscription state; staleness is bounded only by TTL (`roomsubcache.go:54`). broadcast-worker doesn't invalidate this cache (`broadcast-worker/handler.go:66`).
**Impact:** A removed user gets a push for a room they can no longer access — an information leak in sensitive rooms.
**Caveat:** Likelihood hinges on JetStream preserving publish-order between the removal and the message; that ordering is **implicit and untested** here — the verifier flags the missing test as a real concern.
**Detection gap:** Cache metrics don't distinguish stale hits; no delivery-time membership revalidation.
**Mitigation:** Eager/blocking invalidation before candidate selection, or a per-recipient subscription check at delivery; otherwise shrink TTL to 30–60s.

### 3.3 — Concurrent thread-reply deletes → parent tcount undercount
**Summary:** COUNT-then-SET on the parent tcount is non-atomic; interleaved concurrent deletes can write a stale (too-low) count.
**Likelihood:** low · **Severity:** medium · **RISK: medium**
Subsystem: `history-service` thread deletion (also reported by cassandra-storage for the write path).

**Trigger:** Concurrent `DeleteMessage` on replies of the same parent.
**Mechanism:** `countAndSetParentTcount` reads (`countThreadReplies`) then blind-SETs (`setParentTcountAndTlm`) with no CAS between (`write.go:378-392`, two UPDATEs `:359-376`). Interleaving can overwrite a correct count with a stale one.
**Caveat:** Severity **medium, not critical**, likelihood **low** — `LocalQuorum` (`cassutil/cass.go:60`) gives read-after-write consistency that makes a true drop-to-0-with-survivors very unlikely; the realistic outcome is a temporarily stale badge that the next delete recomputes. Thread messages are never lost. The separate cassandra-storage "infinite redelivery loop / phantom count" framing is **refuted** — redelivery is idempotent (blind-SET, no IF), proven by an integration test.
**Detection gap:** No tcount-vs-actual-count audit.
**Mitigation:** Single LWT on the parent row conditioned on the prior count, or fold the recount into one atomic update.

### 3.4 — Federation event ordering / siteID-scoping races `[MERGED: 2 finders, DOWN-RANKED]`
**Summary:** Out-of-order member/role events, or a corrupted/typo'd `user.SiteID`, can mis-route or mis-apply federation updates.
**Likelihood:** low · **Severity:** low–medium · **RISK: low (down-ranked)**
*Merged from* federation-inbox's "out-of-order delivery" and "siteID scoping bug."

**Caveat:** Both are substantially mitigated. **Ordering:** member events share a sequential lane and carry dedup IDs; `role_updated` has a timestamp `$lt` guard and Naks (redelivers) rather than silently losing if the sub doesn't exist yet (`inbox-worker/handler.go:94-110`); the residual only manifests under NATS failover — devs accept it explicitly (comment at `main.go:484`). **siteID:** empty-SiteID publishes are **guarded out** (`room-worker/handler.go:502`, `:1160-1162`), so the "malformed `chat.inbox..external.…` subject" claim is false; only an upstream-corrupted typo'd SiteID could misroute, and that needs prior data corruption, with missing users handled gracefully (WARN + skip).
**Detection gap:** No federation ordering/latency metric; no user-collection SiteID audit.
**Mitigation:** Add `subject.InboxExternal` siteID format validation; extend keyed serialization to `role_updated` per (room,user); periodic SiteID audit.

### 3.5 — Owner-demotion TOCTOU leaves a restricted room with zero owners
**Summary:** A count-then-update gap in restrict handling can demote everyone while the owner is concurrently removed, orphaning the room.
**Likelihood:** low · **Severity:** high · **RISK: medium**
Subsystem: `room-service` `ApplySubscriptionVisibility`.

**Trigger:** Room is restricted while the designated owner is simultaneously unsubscribed/removed.
**Mechanism:** `CountDocuments` (owner exists) then unconditional `UpdateMany` demoting non-owners, non-atomic (`store_mongo.go:1573-1582`). The code comments acknowledge the TOCTOU.
**Caveat:** The code **explicitly accepts** this ("rare, recoverable by retry"); but recovery-by-retry doesn't hold if the owner removal was intentional — the room is then genuinely orphaned. Likelihood low, severity high.
**Detection gap:** No `restricted=true && owners=0` audit; restrict-RPC failures would spike but aren't alerted.
**Mitigation:** Fold the owner-existence check into the aggregation pipeline ($match owner before $set) so a deleted owner yields matched=0 → return `ErrOwnerNotSubscribed`; or wrap in a Mongo transaction.

### 3.6 — Presence Lua type-assertion could drop the "changed" flag `[DOWN-RANKED]`
**Summary:** Bare `res[0].(int64)` assertions would silently yield 0 if Valkey returned a non-int64, suppressing presence-change publishes.
**Likelihood:** low · **Severity:** medium · **RISK: low (down-ranked)**
Subsystem: `user-presence-service` / Valkey Lua.

**Caveat:** Down-ranked from medium — standard go-redis v9 + Valkey always return RESP integers as int64; integration tests pass across scenarios. Real only under a non-standard Redis fork. Still a code-hygiene fix.
**Mitigation:** Validate the assertion and WARN/error on type mismatch; add a periodic reconciliation event.

### 3.7 — Bucket-boundary / clock-skew makes skewed-timestamp messages invisible in room history `[DOWN-RANKED]`
**Summary:** A message whose `created_at` is far skewed lands in a bucket the reader never walks; visible in threads (no bucket) but not the channel.
**Likelihood:** low · **Severity:** high (if triggered) · **RISK: low (down-ranked)**
Subsystem: Cassandra bucketing.

**Caveat:** Down-ranked — gatekeeper sets server timestamps for normal messages, and the read side has a **1-hour clock-skew tolerance**, so only federation replays / clock jumps >1h trigger it. The thread-vs-channel visibility asymmetry is real. Largely a subset of the config-drift risk (1.1) under abnormal timestamps.
**Mitigation:** Validate/clamp `created_at` to ±1h of now at write; cap read `startBucket` to a ceiling+tolerance; audit channel-vs-thread presence.

### 3.8 — Avatar ETag points to deleted MinIO object `[DOWN-RANKED]`
**Summary:** After an out-of-band MinIO delete, stale MongoDB metadata can keep serving 304s / mismatched ETags to cached clients.
**Likelihood:** low · **Severity:** low · **RISK: low**
Subsystem: `avatar-service`.

**Caveat:** Down-ranked — affects only clients with cached content; non-conditional requests get a correct default-SVG fallback; a real re-upload fixes the ETag. The "mismatched ETags" claim is imprecise (they're correct per metadata, just unvalidated against MinIO). Requires operator out-of-band deletion.
**Mitigation:** On `errBlobNotFound` in `serveStored`, delete the stale Mongo record; add a periodic MinIO-vs-Mongo consistency job.

### 3.9 — Avatar EID cache full-flush eviction `[DOWN-RANKED]`
**Summary:** At capacity the EID cache replaces the whole map, which could thrash under a burst.
**Likelihood:** low · **Severity:** low · **RISK: lowest**
**Caveat:** Down-ranked — `EID_CACHE_CAPACITY` defaults to **120,000** (sized for the employee population), so eviction is rare and a miss is just one Mongo query. Lazy TTL cleanup on Get further mitigates. Hygiene-only.
**Mitigation:** LRU or TTL-sweep instead of full flush; add hit/miss metrics.

### 3.10 — Broadcast-worker thread-badge missing NewThreadLastMsgAt `[DOWN-RANKED / RECLASSIFIED]`
**Summary:** Thread-deletion events can carry a null `NewThreadLastMsgAt`, producing an incomplete metadata event.
**Likelihood:** low · **Severity:** low · **RISK: lowest**
**Caveat:** The reported availability/nil-deref mechanism is **refuted** — there is no nil dereference, and sonic marshals nil `*time.Time` safely (explicit test). The genuine residual is a **data-completeness** issue: history-service's soft-delete returns only `newTcount`, not the last-message timestamp, so the event omits it. Reclassified from availability to a minor correctness/completeness gap.
**Mitigation:** Always populate `NewThreadLastMsgAt`; optionally a metric for malformed thread-badge events.

---

## 4. Operational / deploy

### 4.1 — MESSAGE_BUCKET_HOURS drift (deploy dimension)
Cross-reference **§1.1** — the highest-impact operational hazard. The deploy-side controls (required env var, startup parity check / shared source of truth, immutable site-wide constant, CI/CD config validation, startup `message_bucket_hours` metric) belong here. Medium likelihood / critical severity.

### 4.2 — DEV_MODE accidentally enabled in production → auth bypass
**Summary:** `DEV_MODE=true` in prod disables OIDC validation and mints NATS JWTs from an unvalidated account in the request body.
**Likelihood:** medium · **Severity:** high · **RISK: high**
Subsystem: `auth-service`.

**Trigger:** CI/CD or manual config error sets `DEV_MODE=true` in production.
**Mechanism:** `DevMode` → `oidcValidator = nil` (`main.go:59`); `HandleAuth` delegates to `handleDevAuth` (`handler.go:118-120`), which signs a JWT for `req.Account` with no identity check (`:193-215`).
**Impact:** Auth bypass.
**Caveat:** The "mint JWTs for **any** account" claim is **overstated** — the NATS JWT binds the client's public key, so an attacker can only use a JWT for an account whose private key they hold (plus account enumeration). High, not critical-blast-radius. Likelihood medium (defaults to false; requires explicit misconfig).
**Detection gap:** Only a single startup WARN (`main.go:60`); no continuous audit of active auth mode; access log doesn't distinguish dev vs prod path.
**Mitigation:** Remove DEV_MODE from production builds (compile-time flag / separate binary); fail-fast if dev mode requested outside a dev environment; per-request audit log `{mode, account, client_ip}`; rate-limit `/auth`.

### 4.3 — BOOTSTRAP_STREAMS=false + missing streams → CrashLoopBackOff
**Summary:** With bootstrap disabled (correct for prod), a service whose stream wasn't provisioned exits non-zero and crash-loops.
**Likelihood:** low · **Severity:** high · **RISK: medium**
Subsystem: `pkg/stream` bootstrap + all `bootstrapStreams`.

**Trigger:** Prod deploy with `BOOTSTRAP_STREAMS=false` but streams not yet provisioned by ops/IaC.
**Mechanism:** With `enabled=false`, `bootstrapStreams` calls `js.Stream(...)`, errors on missing stream (`broadcast-worker/bootstrap.go:56-60`), and `main` does `os.Exit(1)` (`main.go:148-150`); same across services.
**Caveat:** Not silent — this is **immediate, obvious CrashLoopBackOff** (intentional fail-fast). Likelihood low; standard practice provisions streams via IaC first. Severity high if it happens (service down).
**Detection gap:** Requires reading pod logs; no metric/health signal for "stream missing."
**Mitigation:** Init container that verifies streams exist before the app container starts; startup `bootstrap_streams_verified` metric; document/enforce "streams provisioned before deploy" in CI/CD.

### 4.4 — Unaudited auth failures hamper breach detection `[DOWN-RANKED]`
**Summary:** Auth-failure reasons are spread across logs without a centralized audit trail correlating request-ID → reason → IP → account.
**Likelihood:** medium · **Severity:** medium · **RISK: medium (down-ranked)**
Subsystem: `auth-service` logging.

**Caveat:** The "unlogged/silent" framing is **inaccurate** — `Classify` emits a "request failed" log at INFO with code/reason for every validation failure, and AccessLog logs status + client_ip with request_id; brute force is visible as repeated 401s with the same IP via any log aggregator. The real gaps are **architectural** (separate log entries need aggregation; no per-IP rate limiting; invalid tokens can't expose the attempted account).
**Mitigation:** A dedicated auth-audit log line per outcome; `auth_attempts_total{result, client_ip}` with an alert on invalid-token spikes; rate-limit `/auth`.

### 4.5 — ES template/index dual-ownership drift `[DOWN-RANKED]`
**Summary:** Templates owned by search-sync-worker while indices may be created by ops/IaC could let auto-created monthly indices diverge in schema.
**Likelihood:** low · **Severity:** medium · **RISK: low**
Subsystem: `search-sync-worker`.

**Caveat:** Down-ranked — the report **conflates** ES per-index explicit mappings with composable templates; ES 7+ has no "per-index templates," and there's no evidence of ad-hoc explicit-mapping index creation in the codebase. A design-level first-message verification guard exists (unimplemented).
**Mitigation:** Single ownership of templates; validate template at worker startup; version-namespace templates.

### 4.6 — search-sync-worker stale FilterSubjects miss new INBOX subjects `[DOWN-RANKED]`
**Summary:** If ops adds an INBOX subject and the worker's compiled `FilterSubjects` is stale, matching events are silently skipped.
**Likelihood:** low · **Severity:** medium · **RISK: low**
**Caveat:** The "stream schema drift" title is misleading — the worker never bootstraps INBOX (guarded). The real hazard is a **consumer-filter** mismatch requiring both ops expanding subjects and a stale deploy; spotlight/user-room only index member events, which are scoped and stable, so impact is narrow.
**Mitigation:** Make FilterSubjects env-configurable, or consume all INBOX and filter by type in `BuildAction`.

### 4.7 — Bulk result-count mismatch nak-all `[DOWN-RANKED]`
**Summary:** If ES returns fewer bulk results than actions, the worker naks the whole batch.
**Likelihood:** low · **Severity:** low · **RISK: lowest**
**Caveat:** Largely refuted — ES guarantees one-result-per-action; a truncated response fails JSON parsing earlier; the LWW script is atomic. This is a **safe defensive guard**; worst case is harmless reprocessing.
**Mitigation:** Add a nak-all metric; document idempotent rerun safety.

### 4.8 — Multi-service stream bootstrap race `[REFUTED — informational]`
**Summary:** Claimed that concurrent dev `bootstrapStreams` calls deadlock/stall via the 25s shutdown timeout.
**Verdict:** Effectively **refuted.** `CreateOrUpdateStream` is idempotent on concurrent identical calls and bootstrap uses `context.Background()` (no deadline), so the 25s shutdown timeout is irrelevant; concurrent and sequential calls both succeed; failures are immediate. Listed only for completeness; **no action required** beyond optionally designating a single dev stream owner.

---

## Cross-cutting observations

1. **Silent degradation is the systemic pattern.** Nearly every high-risk finding shares one shape: an operation reports success (HTTP 200 / Ack / empty result) while the result is incomplete — config-drift history misses, presence-offline fallback, CCS empty hits, dropped federation member-adds, stale-cache notifications. The codebase consistently lacks the metric/alert that would convert silent into observable.
2. **MESSAGE_BUCKET_HOURS is a single point of operational fragility.** Six finders independently surfaced it. The math is correct; the failure is purely a deploy-discipline gap with catastrophic, invisible consequences. It deserves a hard technical guard rather than documentation alone.
3. **Cross-site federation lacks reconciliation.** Multiple distinct findings (1.2, 2.1, 2.3, 2.8, 3.1, 3.4) reduce to "two sites diverge and nothing notices." There is no periodic cross-site audit of membership, roles, or presence.
4. **Cassandra multi-table writes have no atomicity story.** Unlogged batches and sequential mirror updates (1.4, 3.3) trade atomicity for throughput but have no repair/audit backstop.
5. **Verifier corrections meaningfully deflated several "critical data-loss" claims.** Gatekeeper Ack-dup, DM fire-and-forget, key-fanout, and shutdown-timeout are reliability/duplicate-work issues with self-healing paths — not silent permanent loss. Resist the instinct to treat every "message disappears" report as catastrophic.
6. **history-service is the weakest HTTP citizen** — it omits the HandlerTimeout that user-service applies, and the same per-query-timeout gap recurs in its thread-count path.

## Recommended next steps (ordered by ROI)

1. **Lock down MESSAGE_BUCKET_HOURS** — make it a required env var, validate parity at startup against a shared source of truth (fail FATAL on mismatch), emit it as a startup metric, and enforce it as an immutable site-wide constant in IaC/CI. *(Closes 1.1 / 4.1 — highest aggregate risk, low effort.)*
2. **Add a federation health check** verifying each remote INBOX stream exists (startup + periodic), and alert on it. *(Closes 2.1; foundation for federation observability.)*
3. **Add HandlerTimeout to history-service** and propagate the deadline into gocql queries. *(Closes 2.2; one-line middleware + context plumbing.)*
4. **Fix the cross-site restrict-before-add bypass** — have `handleMemberAdded` apply current room restrict state to new subscriptions. *(Closes 3.1; access-control correctness.)*
5. **Remove DEV_MODE from production builds** (compile-time flag / separate binary) and add a fail-fast environment guard. *(Closes 4.2; eliminates an auth-bypass class.)*
6. **Make absent-user federation member-adds retriable** (Nak instead of silent skip) and escalate the log to ERROR + counter. *(Closes 1.2.)*
7. **Surface presence degradation** with a `confident` flag on the query response and alert on peer-failure rate. *(Closes 2.3.)*
8. **Add an init container that verifies required streams exist** before app containers start. *(Closes 4.3.)*
9. **Make notification-worker membership invalidation eager** (or revalidate subscription at delivery / shrink TTL to 30–60s). *(Closes 3.2 info-leak.)*
10. **Stand up a periodic cross-site reconciliation job** (membership, roles, presence) and a Cassandra `messages_by_id` vs `messages_by_room` / tcount-vs-actual consistency audit. *(Catches 1.4, 3.1, 3.3, 1.2 residuals.)*
11. **Application-level store-operation timeouts + a post-`wg.Wait` poison-Ack hook** in JetStream workers; size MAX_WORKERS to handler latency. *(Mitigates 2.7, 2.8.)*
12. **Wire missing metrics/alerts** — auth-failure-by-IP, CCS skipped-clusters, FanoutErrors, nak-all rate, JWKS rotation, Ack-failure counter. *(Broad detection-gap closure; lower per-item ROI but cumulatively high.)*
13. **Hygiene/defensive backlog (low priority):** persisted upload-attachment TTL + sweep (1.3); validate presence Lua type assertions (3.6); LRU/TTL eviction for the EID cache (3.9); avatar stale-record cleanup (3.8); check `jobguard.Guard` return value (2.9). Defer 4.5–4.8 as informational/refuted.
