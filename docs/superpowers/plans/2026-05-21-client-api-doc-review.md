# Client API Doc Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply a single edit pass to `docs/client-api.md` that brings the frontend integration contract back to ground truth, based on the structured findings reports from 11 read-only agents (5 RPC services + 6 event-producing workers).

**Architecture:** Aggregation-then-edit. The 11 agent reports (already produced or in flight) are the authoritative input. The implementer collects every finding, organizes it by client-api.md section, applies edits in a single pass, shows the user the diff, and commits + pushes after approval. No code changes. One commit on `claude/client-api-doc-review-Rz70X`.

**Tech Stack:** Markdown editing only. No build, no tests to run.

---

## Spec

Full design at `docs/superpowers/specs/2026-05-21-client-api-doc-review-design.md`.

## Background for the implementer

- The branch `claude/client-api-doc-review-Rz70X` is checked out and has the spec commit already pushed.
- 11 read-only research agents were dispatched in parallel; each produces a markdown report against its service. Reports completed and in the conversation transcript so far (as of plan-writing): **auth-service, search-service, message-gatekeeper, notification-worker, message-worker, search-sync-worker, broadcast-worker, inbox-worker**. Still in flight: **room-service, history-service, room-worker**.
- The target file is `docs/client-api.md` (2012 lines as of plan-writing). Only this file is edited.
- CLAUDE.md §5 ("Before Committing") requires the doc to stay in sync with client-facing handlers. This pass enforces that retroactively.
- The user has explicitly chosen the workflow:
  - Agents are research-only; implementer aggregates.
  - Scope: RPC services + event-producing workers.
  - Depth: field-level + event-level.
  - Show diff before committing/pushing.

### File map

- Modify: `docs/client-api.md` (single file).
- No other files change. No code edits. No new files.

---

## Task 1: Wait for the three remaining agent reports

**Files:** none.

The implementer cannot start aggregation until every report is in. The conversation harness delivers each report as a `<task-notification>` event; do not poll or sleep, just wait.

- [ ] **Step 1: Confirm which reports are still pending**

Pending: `room-service`, `history-service`, `room-worker`. (Sanity-check against the transcript before proceeding.)

- [ ] **Step 2: Wait for completion notifications**

Each pending agent will deliver a `<task-notification status="completed">` message. Do nothing until all three have arrived.

- [ ] **Step 3: Re-skim each completed report**

Scroll back through the transcript and confirm each of the 11 reports is present and complete. If any agent failed or returned an incomplete report, re-dispatch a fresh agent with the same prompt before proceeding to Task 2.

---

## Task 2: Build the consolidated changeset

**Files:** none (working notes only — do NOT write to a file).

This task produces the in-context list of edits the implementer will apply in Task 3+. Keep it as a structured working summary in the assistant's reply, NOT as a markdown file in the repo — the changeset is throwaway scratch.

- [ ] **Step 1: Open `docs/client-api.md` and read it end-to-end with the reports beside it**

Use the Read tool with no `limit` (or chunks of 500 lines) to walk the whole file. Cross-reference each section against the relevant report(s).

- [ ] **Step 2: Group findings by doc section**

Produce a working list organized by section heading:

- §1 Overview / Subject placeholders / Reply patterns
- §2.1 NATS connection (recommended baseline subscriptions)
- §2.2 POST /auth
- §3.1 room-service (per-RPC)
- §3.2 history-service (per-RPC)
- §3.3 search-service (per-RPC)
- §4 Message Send
- §5 Server-Pushed Events
- §6 Error envelope

For each group, list:
  - **Discrepancies** (doc wrong, code right) → must fix.
  - **Missing** (undocumented but client-visible) → must add.
  - **Doc-only** (documented but doesn't exist) → must remove.
  - **Cross-cutting** (e.g. notification recipient policy) → fix once.

- [ ] **Step 3: Resolve cross-report contradictions**

If two reports disagree (e.g. gatekeeper says broadcast-worker publishes X to subject Y; broadcast-worker report says it publishes to subject Z), re-read the cited file:line in the actual code via the Read tool to resolve. If still ambiguous, raise to the user with the conflicting citations before continuing.

- [ ] **Step 4: Drop pure-stylistic suggestions**

The spec limits scope to correctness/completeness. Discard any "could be more concise" / "consider renaming" findings. Keep only:
  - Wrong subject strings
  - Wrong field names / types / required-flags
  - Wrong / missing error strings or codes
  - Wrong / missing triggered events
  - Wrong recipient or attribution claims
  - Missing handlers / fields / behaviors

- [ ] **Step 5: Present the consolidated changeset to the user**

Show a single message listing all proposed edits, organized by doc section. For each edit:
  - Section + approximate line range
  - One-line summary of the change
  - Citation: `<service>/<file>:<line>` for the source of truth

Ask: "Apply these edits, or revise the changeset?"

Do NOT proceed to Task 3 until the user approves.

---

## Task 3: Apply edits — §1, §2.1, §2.2 (Overview / Connection / Auth)

**Files:**
- Modify: `docs/client-api.md` (sections §1, §2.1, §2.2 — roughly lines 1–181 as of plan-writing).

Concrete edits known at plan-writing time (from auth-service and message-gatekeeper reports):

- [ ] **Step 1: Fix §2.1 recommended baseline subscription subject**

The doc recommends `chat.room.{roomID}.stream.msg` per room. Code publishes `chat.room.{roomID}.event` (see gatekeeper report D3 / broadcast-worker report). Find the line in the §2.1 "Recommended baseline subscriptions on connect" block and replace `chat.room.{roomID}.stream.msg` with `chat.room.{roomID}.event` (and update the phrasing — it's not just "new messages", it's all room events including edits/deletes for channel rooms).

- [ ] **Step 2: Add §2.2 POST /auth — second 400 error string**

Add a second row or note to the 400 table: `{"error": "invalid natsPublicKey format"}` (emitted by `auth-service/handler.go:84` and dev-mode `:141`).

- [ ] **Step 3: Add §2.2 POST /auth — second 401 error string**

Add a row for non-expired invalid SSO token: `{"error": "invalid SSO token"}` (emitted by `auth-service/handler.go:96`).

- [ ] **Step 4: Add §2.2 POST /auth — dev-mode error string**

In the Dev mode subsection, add a note: in dev mode the 400 missing-fields error string is `"account and natsPublicKey are required"` (different from prod). Source: `auth-service/handler.go:136`.

- [ ] **Step 5: Add §2.2 POST /auth — dev-mode response shape**

In Dev mode: clarify the response synthesizes `email = account + "@dev.local"`, `engName = account`, and leaves `employeeId`, `chineseName`, `deptName`, `deptId` empty strings. Source: `auth-service/handler.go:154-161`.

- [ ] **Step 6: Add §2.2 POST /auth — JWT expiry note**

Add: "The returned `natsJwt` has a server-configured lifetime (default 2h via `NATS_JWT_EXPIRY`). Clients should refresh before expiry by re-calling `POST /auth`." Source: `auth-service/main.go:23`.

- [ ] **Step 7: Verify §1 federation claim is sharpened**

Per the inbox-worker report, the §1 line "Federation arrivals … remain backend-internal" is accurate but the framing could mislead. Update it to: "Cross-site events look identical to local events on the user/room subjects — the federation layer (OUTBOX/INBOX) is invisible to clients; cross-site member adds, role updates, etc. arrive on the same `chat.user.{account}.…` subjects as same-site ones."

- [ ] **Step 8: Show the §1–§2.2 diff to the user**

Run `git diff docs/client-api.md` and show. Wait for go-ahead before Task 4.

---

## Task 4: Apply edits — §3.1 room-service

**Files:**
- Modify: `docs/client-api.md` §3.1 (roughly lines 184–~900 as of plan-writing).

Concrete edits at plan-writing time depend on the in-flight room-service and room-worker reports. The implementer MUST have those reports in hand before this task starts.

- [ ] **Step 1: Apply per-RPC edits from the room-service report**

For each RPC the report flags (Create Room, List Rooms, Get Room, Add Members, Remove Member, Update Member Role, Mark Read, Subscribe/Unsubscribe, Hide/Open, Read Receipt, …): apply subject-string, request-field, response-field, and error-message corrections exactly as the report cites them. One edit per finding, in doc order.

- [ ] **Step 2: Apply per-RPC triggered-event corrections from the room-worker report**

For each async-job RPC (Add Members, Remove Member, etc.): correct the "Triggered events — success path" lists. Specifically verify the AsyncJobResult event subject, payload, and the "must set X-Request-ID" requirement.

- [ ] **Step 3: Fix the Mark Messages Read note about cross-site visibility**

Per the inbox-worker report, the existing line at `client-api.md:777` (or thereabouts) — "(Cross-site users may observe a delayed `subscription.update` …)" — is wrong. Replace with: "Cross-site users' home-site Mongo `lastSeenAt`/`alert` is updated silently by `inbox-worker`; no `subscription.update` event is emitted to clients."

- [ ] **Step 4: Add undocumented RPCs flagged by the room-service report**

Insert new method sections in alphabetical / logical order, matching the existing format (Subject / Reply / Request body / Success / Error / Triggered events).

- [ ] **Step 5: Remove or correct doc-only RPCs flagged by the report**

If the report lists any RPC documented in §3.1 that has no matching handler, either remove it or mark it explicitly deprecated, per the user's decision (ask if unclear).

- [ ] **Step 6: Show the §3.1 diff**

`git diff docs/client-api.md` → show user → wait for go-ahead before Task 5.

---

## Task 5: Apply edits — §3.2 history-service

**Files:**
- Modify: `docs/client-api.md` §3.2.

Depends on the in-flight history-service report. Recent commit `c0d023f` (JetStream edit/delete + unified canonical `Nats-Msg-Id`) is the area most likely to need updates.

- [ ] **Step 1: Apply per-RPC edits from the history-service report**

For each documented method (list/get message, edit, delete, thread list, surrounding, etc.): apply subject/field/error corrections per the report's citations.

- [ ] **Step 2: Apply Edit Message and Delete Message "Triggered events" corrections from the broadcast-worker report**

Per the broadcast-worker report (D1–D5):

  - Rewrite the Edit Message "Triggered events — success path" table to nest fields under `messageEdited`:
    - `messageId` → `messageEdited.messageId`
    - `newMsg` → `messageEdited.newContent`
    - `encryptedNewMsg` → `messageEdited.encryptedNewContent`
    - `editedBy` (number) → `messageEdited.editedBy` (string account)
    - `editedAt` (number ms) → `messageEdited.editedAt` (RFC 3339 string)
    - Add `messageEdited.updatedAt` (RFC 3339 string)
  - Same nesting for Delete Message under `messageDeleted`.
  - Add a second "Triggered events" block under both Edit and Delete: "For DM/BotDM rooms — `chat.user.{recipient}.event.room` — same `RoomEvent` payload, published once per non-bot member of the DM." (Source: `broadcast-worker/handler.go:188-207`.)
  - Note that the RoomEvent envelope on edit/delete carries zero-valued base fields (`roomName: ""`, `userCount: 0`, `lastMsgAt: "0001-01-01T00:00:00Z"`, `lastMsgId: ""`) — instruct clients to ignore them. (Source: `pkg/model/event.go:172-177`.)

- [ ] **Step 3: Add undocumented history RPCs flagged by the report**

Same format as §3.1.

- [ ] **Step 4: Show the §3.2 diff**

`git diff docs/client-api.md` → show user → wait for go-ahead before Task 6.

---

## Task 6: Apply edits — §3.3 search-service

**Files:**
- Modify: `docs/client-api.md` §3.3 (roughly lines 1475–1735 as of plan-writing).

Concrete edits from the search-service report:

- [ ] **Step 1: Add CCS note to `search.messages`**

Under §3.3 `search.messages`, add a sentence: "Results may include messages from rooms hosted on remote sites (federated via Elasticsearch's cross-cluster search). The `siteId` field on each `SearchMessage` distinguishes the originating site; there is no client opt-in/opt-out." Source: `search-service/query_messages.go:18` (`MessageIndexPattern = ["messages-*", "*:messages-*"]`).

- [ ] **Step 2: Fix `search.users` request-body claim**

Doc says "the third-party endpoint hardcodes offset=0, limit=25." Actual placeholder client sends only `{"query": "..."}` with no pagination args. Either delete the offset/limit sentence or rephrase to "the placeholder client today sends only `{query}`; pagination args will be added when the production endpoint contract is finalized." Source: `search-service/users_client.go:48-50`.

- [ ] **Step 3: (Optional) Add JSON-unmarshal-failure error envelope note**

In §3.3's error sections, note: "When the request body fails to unmarshal, the reply is `{\"error\": \"invalid request payload\"}` with no `code` field." Source: `pkg/natsrouter/register.go:20`. (Already covered generically in §6, so this is low-priority — skip unless the user asks for it.)

- [ ] **Step 4: Show the §3.3 diff**

`git diff docs/client-api.md` → show user → wait for go-ahead before Task 7.

---

## Task 7: Apply edits — §4 Message Send

**Files:**
- Modify: `docs/client-api.md` §4 (roughly lines 1737–1932 as of plan-writing).

Concrete edits from the message-gatekeeper, broadcast-worker, and notification-worker reports:

- [ ] **Step 1: Remove non-existent request fields**

Doc lists `attachments`, `reply-to`, `mentions` as request fields. None exist in `model.SendMessageRequest` (`pkg/model/message.go:27-34`). Mentions are parsed downstream from `content` in broadcast-worker. Delete these rows from the request table.

- [ ] **Step 2: Add a `requestId` warning**

Add to the §4 request schema notes: "If `requestId` is empty or omitted, the server silently skips sending a reply and the client will time out. `requestId` must be non-empty (the doc recommends 36-char hyphenated UUIDv7 for log correlation; the server itself accepts any non-empty string)." Source: `message-gatekeeper/handler.go:109-112`.

- [ ] **Step 3: Enumerate validation rules**

Add to §4 a "Validation" subsection (or expand existing one) listing all 11 gatekeeper validation rules per the gatekeeper report:
  1. siteID in subject must match the gatekeeper's site.
  2. Payload JSON-valid against `SendMessageRequest`.
  3. `id` base62 (20-char preferred; 17-char accepted for legacy).
  4. `threadParentMessageId` base62 if set.
  5. `content` non-empty.
  6. `content` ≤ 20480 bytes (20 KiB).
  7. `threadParentMessageCreatedAt` required when `threadParentMessageId` set.
  8. Sender must be subscribed to the room.
  9. Large-room cap: non-owner/admin/bot senders rejected when `room.userCount > LARGE_ROOM_THRESHOLD` (default 500), unless the send is a thread reply.
  10. Quote thread-context match: if `quotedParentMessageId` set, the parent's thread must match this message's thread (or both must be main-room).
  11. (No rate limiting, no attachment validation, no mention validation, no duplicate-id check — call this out explicitly.)

- [ ] **Step 4: Enumerate error responses**

Replace the existing partial error list with the complete 11-entry table from the gatekeeper report (siteID mismatch, unmarshal failure, invalid message ID, invalid thread parent message ID, empty content, content exceeds maximum size, missing thread timestamp, not subscribed, large-room restricted with `code: "large_room_post_restricted"`, quoted-parent fetch error, quoted-parent thread mismatch).

- [ ] **Step 5: Document infra-error no-reply behavior**

Add: "Infra errors (subscription store error, room-meta store error, MESSAGES_CANONICAL publish failure) cause the gatekeeper to NAK the message; the client receives no reply and must rely on request timeout + retry." Source: `message-gatekeeper/handler.go:79-83`.

- [ ] **Step 6: Document JetStream dedup window**

Add: "The MESSAGES_CANONICAL publish uses `Nats-Msg-Id = <messageID>` for dedup. Resending the same `msg.send` with the same `id` is idempotent within the canonical stream's dedup window (a stream config — clients should still treat retries as best-effort)." Source: `pkg/natsutil/canonical_dedup.go:18-27`.

- [ ] **Step 7: Document the BotDM skip in broadcast-worker**

Add to §4 "Triggered events": "For `botDM` rooms, the `new_message` RoomEvent is NOT fanned out (broadcast-worker's `handleCreated` only recognizes `channel` and `dm`). Bots receive messages via the BotDM-specific flow only." Source: `broadcast-worker/handler.go:113-120`.

- [ ] **Step 8: Fix notification recipient policy**

In the §4 "Triggered events" block for `chat.user.{recipient}.notification`, rewrite the recipient policy. Today's doc says "typically the DM partner for DMs, and any `@`-mentioned user for channel messages." Code sends to **every room subscriber except the sender**, with no mention/DM filtering and no `DisableNotification` filtering. Replace with: "Every member of the room except the sender, regardless of room type (channel, dm, botDM, discussion) or mention status. The `subscription.disableNotification` field is persisted on botDM re-subscribe but **not yet honored** — no service currently filters on it." Source: `notification-worker/handler.go:42-69`.

- [ ] **Step 9: Add note that notification payload is bare Message (not ClientMessage)**

Under the notification payload schema add: "Note: `message` is the bare `Message` struct (no `sender` Participant enrichment — that is only on `chat.user.{account}.event.room`)." Source: `pkg/model/event.go:70-75`.

- [ ] **Step 10: Show the §4 diff**

`git diff docs/client-api.md` → show user → wait for go-ahead before Task 8.

---

## Task 8: Apply edits — §5 Server-Pushed Events and §6 Error envelope

**Files:**
- Modify: `docs/client-api.md` §5, §6.

- [ ] **Step 1: Verify §5 RoomKeyEvent is still accurate**

Re-read §5.1 "Room Encryption Keys" against the current `pkg/model` event types and any worker publishing to `chat.user.{account}.event.room.key`. If any report (room-service is likely) flagged drift, apply.

- [ ] **Step 2: Verify §6 error envelope shape**

The §6 envelope is `{error, code?}`. Confirm the `code` field is documented as optional and that `large_room_post_restricted` is added to the list of known code values (if any list exists). Source: `pkg/model/error.go`.

- [ ] **Step 3: Show the §5–§6 diff**

`git diff docs/client-api.md` → show user → wait for go-ahead before Task 9.

---

## Task 9: Full-doc review and final-diff approval

**Files:**
- Modify: `docs/client-api.md` (final touch-ups only).

- [ ] **Step 1: Read `docs/client-api.md` end-to-end**

Use the Read tool. Look for:
  - Broken markdown (unclosed code fences, mismatched table columns).
  - References to old field names that the edit pass missed.
  - Internal cross-references that point to renamed/removed sections.
  - "Triggered events — success path: `None — reply only.`" entries that should now list events (or vice versa).

- [ ] **Step 2: Apply touch-up edits**

Fix anything found in Step 1. Each edit individually small.

- [ ] **Step 3: Show the full diff to the user**

```bash
git diff docs/client-api.md
```

Wait for explicit approval before Task 10.

---

## Task 10: Commit and push

**Files:**
- Modify: `docs/client-api.md` (already edited).

- [ ] **Step 1: Stage the doc**

```bash
git add docs/client-api.md
```

- [ ] **Step 2: Commit**

```bash
git commit -m "$(cat <<'EOF'
docs(client-api): align with current code after multi-agent verification

Apply field-level + event-level corrections from a parallel 11-agent
verification pass (5 RPC services + 6 event-producing workers).
Fixes wrong subjects, wrong/missing field names, wrong/missing error
messages, wrong notification recipient policy, wrong edit/delete
payload shape, and undocumented client-visible triggered events.
EOF
)"
```

- [ ] **Step 3: Push**

```bash
git push -u origin claude/client-api-doc-review-Rz70X
```

Expected: `branch 'claude/client-api-doc-review-Rz70X' set up to track 'origin/claude/client-api-doc-review-Rz70X'.` (or just a fast-forward push since the spec commit is already on the remote).

- [ ] **Step 4: Confirm push success**

```bash
git status
```

Expected: `Your branch is up to date with 'origin/claude/client-api-doc-review-Rz70X'.` and `nothing to commit, working tree clean.`

---

## Out of scope (do NOT do)

- Opening a PR (the user will do that separately, or request it explicitly).
- Re-running the agents (each report is single-shot research; conflicts go through the user via Task 2 Step 3).
- Editing `nats-subject-naming.md`, `cassandra_message_model.md`, or any other doc.
- Adding rate limiting, attachment validation, mention validation, or duplicate-id checks to gatekeeper — those are absent by design and the doc should reflect that absence, not fix it.

---

# Revision 2 — code + doc execution (supersedes Tasks 3–10 above)

Scope expanded to code + doc per the spec's Revision 2. Tasks 2-doc (the
consolidated changeset) are still valid as the source of doc edits, but the
"no code changes" / "single file" framing is replaced by the phases below.
Execute phases in order. Each code task is TDD (Red → Green → Refactor →
commit) and must pass `make lint` + `make test SERVICE=<svc>` before commit.

## Phase A — room-service: remove List Rooms + Get Room RPCs

**Files:** `room-service/handler.go`, `room-service/store.go`,
`room-service/store_mongo.go`, `pkg/subject/subject.go`,
`pkg/model/room.go` (ListRoomsResponse), `room-service/handler_test.go`,
`room-service/*_test.go`.

- [ ] Remove `natsListRooms`, `natsGetRoom` handlers + their `QueueSubscribe`
  lines in `RegisterCRUD`.
- [ ] Remove `store.ListRooms` interface method + Mongo impl. KEEP
  `store.GetRoom` (used at handler.go:491,589,671,1075) and `ListRoomsByIDs`.
- [ ] Remove `RoomsList`, `RoomsListWildcard`, `RoomsGet`, `RoomsGetWildcard`
  subject builders. KEEP parsing helpers if still referenced.
- [ ] Remove `model.ListRoomsResponse` if unused elsewhere.
- [ ] Regenerate mocks (`make generate SERVICE=room-service`) — store iface changed.
- [ ] Delete/adjust tests referencing the removed handlers.
- [ ] `make lint` + `make test SERVICE=room-service`; commit.

## Phase B — room-service: sanitizeError allow-list

**Files:** `room-service/helper.go`, `room-service/handler.go`,
`room-service/helper_test.go` (or handler_test.go).

- [ ] RED: add a table test asserting `sanitizeError` returns the verbatim
  message for each of: `exactly one of account or orgId must be set`,
  `cannot remove the last member of the room`, `last owner cannot leave the
  room`, `org members cannot leave individually`, `room ID mismatch`,
  `remove-member only supported on channel rooms, got X`, `limit must be > 0`,
  `offset must be >= 0`. Confirm it fails (returns "internal error").
- [ ] GREEN: introduce sentinels for the exact-match strings, wrap the
  formatted one (`remove-member only supported...: got %s`) with `%w`, change
  the call sites (handler.go:460,463,503,513,529,532,496,485,583,688) to
  return the sentinels, add them to the `errors.Is` block in `sanitizeError`.
- [ ] `make lint` + `make test SERVICE=room-service`; commit.

## Phase C — message-gatekeeper: requestId validation + /simplify

**Files:** `message-gatekeeper/handler.go`, `message-gatekeeper/handler_test.go`.

- [ ] Read `processMessage` / `sendReply` to see how `requestId` is consumed
  (handler.go:103-117) and where validation should live.
- [ ] RED: add tests for the new validation (empty requestId, non-UUID
  requestId) asserting the chosen behavior. Confirm fail.
- [ ] GREEN: implement the requestId validation check (validate it is a
  non-empty valid UUID via `idgen.IsValidUUID`; on invalid, take the agreed
  action). Confirm tests pass.
- [ ] Run `/simplify` on the touched gatekeeper code; re-run tests.
- [ ] `make lint` + `make test SERVICE=message-gatekeeper`; commit.

## Phase D — broadcast-worker + pkg/model: flattened edit/delete events

**Files:** `pkg/model/event.go`, `pkg/model/model_test.go`,
`broadcast-worker/handler.go`, `broadcast-worker/handler_test.go`,
`chat-frontend` decoder if it consumes these.

- [ ] Grep consumers of `messageEdited`/`messageDeleted` (frontend, tests) to
  learn the required fields.
- [ ] RED: model round-trip test for `EditRoomEvent` / `DeleteRoomEvent`
  (flattened: type, roomId, timestamp, siteId, + edit/delete fields only).
- [ ] GREEN: define the structs; update `fanOutMutationEvent` to publish them
  on `chat.room.{roomID}.event` (channel) and `chat.user.{recipient}.event.room`
  (DM/botDM). Confirm tests pass.
- [ ] Update frontend decoder + its tests if needed.
- [ ] `make lint` + `make test SERVICE=broadcast-worker` + model tests; commit.

## Phase E — search-service: offset/limit on search users

**Files:** `pkg/model/search.go` (SearchUsersRequest),
`search-service/handler.go`, `search-service/users_client.go`,
`search-service/*_test.go`.

- [ ] RED: test that the users client forwards `offset`/`limit` (default
  0/25) to the HR endpoint payload.
- [ ] GREEN: add `offset`/`limit` to `SearchUsersRequest`, normalize, forward
  in `users_client.go`. Confirm tests pass.
- [ ] `make lint` + `make test SERVICE=search-service`; commit.

## Phase F — chat-frontend: remove getRoom usage

**Files:** `chat-frontend/src/components/.../MessageActionMenu/MessageActionMenu.jsx`
(+ test), `chat-frontend/src/context/RoomEventsContext/useRoomSubscriptions.js`,
`chat-frontend/src/api/getRoom/` (delete), `chat-frontend/src/api/index.ts`,
`chat-frontend/src/api/_transport/subjects.ts` (remove `roomsGet`/`roomsList`).

- [ ] Investigate both call sites; pick replacement source for userCount
  (MessageActionMenu already has `room.userCount` prop) and for the
  subscription-update metadata fetch (the subscription.update event payload).
- [ ] Update tests first (RED), then implementation (GREEN).
- [ ] `npm run typecheck` + `npm test` in chat-frontend; commit.

## Phase G — doc rewrite

**Files:** `docs/client-api.md` only.

- [ ] Apply ALL field/error/event/subject corrections from the Task-2 changeset.
- [ ] Remove federation/cross-site/backend-internal content (§1 and inline).
- [ ] Remove Dev mode from §2.2.
- [ ] Remove server-to-server (`chat.server.>`) RPCs.
- [ ] Remove the standalone Room schema; replace each RPC's response with the
  verified handler shape.
- [ ] Remove List Rooms / Get Room sections.
- [ ] Restructure: delete §5; move each `RoomKeyEvent` into Create/Add/Remove
  member "Triggered events"; add a "Room Encryption" client-behavior section.
- [ ] Rewrite §4 message-send fan-out from `publishChannelEvent` /
  `publishDMEvents` / `buildClientMessage`; drop the notification event.
- [ ] Rewrite Create Room (subject, request, `CreateRoomSyncReply`, dm-exists).
- [ ] Fix AsyncJobResult schema, subscription `id`/`u` keys, edit/delete shapes.
- [ ] Verify EVERY remaining RPC response against its handler before writing.
- [ ] Show full diff; commit.

## Phase H — final verification + push

- [ ] `make lint` + `make test` (all) green.
- [ ] `git diff` review of every changed file.
- [ ] Show diffs to user; on approval `git push -u origin claude/client-api-doc-review-Rz70X`.
