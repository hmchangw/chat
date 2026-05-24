# Client API Documentation Review — Design

## Purpose

`docs/client-api.md` is the frontend integration contract for the chat
backend. Drift between the doc and the actual code (subject strings, field
names, error messages, triggered events) breaks client integrators. This
pass audits the entire client-facing surface against the current code and
produces a single edit pass that brings the doc back to ground truth.

The audience is the same as the doc itself: a developer writing a client
(web, mobile, or third-party). Their integration is correct only if every
field, subject, error, and triggered event in the doc matches the
implementation.

## Scope

In scope:

- The full content of `docs/client-api.md` as it stands today.
- Every Go service that either (a) registers a client-facing NATS handler
  (`chat.user.{account}.…` request or message-send subjects) or HTTP route,
  or (b) publishes a server-pushed event a client receives.
- Field-level verification: name, type, required-flag, validation rules,
  derivation rules, default values.
- Event-level verification: every "Triggered events — success path" / "—
  error path" entry must match the publishes actually performed in code.
- Discovery: undocumented handlers, fields, error paths, or events that
  clients could observe today.

Out of scope:

- Stylistic rewrites or restructuring sections that are already accurate.
- Backend-only subjects (MESSAGES, MESSAGES_CANONICAL, OUTBOX, INBOX,
  ROOMS) and server-to-server subjects.
- The contract itself — this pass corrects the description of what exists,
  it does not propose API changes.
- Anything not currently in `docs/client-api.md` and not client-visible.

## Approach

Eleven parallel read-only research agents, one per service, each producing
a structured findings report. I aggregate the reports into a single edit
pass on `docs/client-api.md`.

### Agent inventory

RPC services (5):

| Agent | Service path | Doc section |
|-------|--------------|-------------|
| auth | `auth-service/` | §2.2 `POST /auth` |
| room | `room-service/` | §3.1 |
| history | `history-service/` | §3.2 |
| search | `search-service/` | §3.3 |
| gatekeeper | `message-gatekeeper/` | §4 Message Send |

Event-producing workers (6):

| Agent | Service path | Why |
|-------|--------------|-----|
| broadcast | `broadcast-worker/` | Fans messages/edits/deletes to `chat.room.{roomID}.stream.msg`. |
| notification | `notification-worker/` | Publishes mention/DM notifications to `chat.user.{account}.notify`. |
| room-worker | `room-worker/` | Async-job results + member events fired by Add Members and other async RPCs. |
| message-worker | `message-worker/` | Sanity-check — confirm it publishes no client-visible events. |
| inbox | `inbox-worker/` | Verify the "federation arrivals remain backend-internal" claim. |
| search-sync | `search-sync-worker/` | Sanity-check — confirm no client-visible events. |

### Agent prompt shape

Every agent prompt enforces the same contract:

- READ-ONLY. No edits, no commits, no further agent spawns.
- Field-level + event-level verification depth.
- Cite `file:line` for every claim.
- Output a markdown report with these sections:
  - Confirmed correct
  - Discrepancies (doc wrong)
  - Missing from doc
  - Doc should remove
  - Questions / uncertain
  - For RPC agents: organized by RPC method.
  - For worker agents: an inventory table of client-visible publishes.

### Aggregation

Once all reports are in:

1. I assemble a consolidated changeset organized by doc section.
2. I show the user the changeset (either a summary or the literal diff,
   depending on size) before applying.
3. After user approval, I apply edits to `docs/client-api.md` in a single
   pass to preserve voice and formatting consistency.
4. I show the resulting diff and ask before committing/pushing to
   `claude/client-api-doc-review-Rz70X`.

## Verification rules

For each RPC, the agent verifies:

| Element | Verification |
|---------|--------------|
| Subject string | Literal match against `nc.QueueSubscribe` / `natsrouter.Register` call (use `pkg/subject` builders to resolve patterns). |
| Reply pattern | Standard `_INBOX.>` vs. async `chat.user.{account}.response.{requestID}` — matches code. |
| Request field | Name, type, required-flag, validation, defaults match the Go struct. |
| Server-derived fields | Doc's "derived from subject / SSO claim / etc." claims match code. |
| Success response field | Name, type, optionality match the Go response struct. |
| Error response | Each documented error string appears verbatim in code; no undocumented client-reachable error paths. |
| Triggered events (success) | Every `nats.Publish` / `js.Publish` / `PublishMsg` to a `chat.user.*` or `chat.room.*` subject is listed; subject patterns and payload types match; events attributed to the right service. |
| Triggered events (error) | Same. |

For each worker, the agent enumerates every publish call and classifies
its subject as client-visible (`chat.user.*` / `chat.room.*`) or
backend-internal (`outbox.*`, `chat.server.*`, internal streams), then
cross-references the client-visible set against the doc.

## Output of this pass

A single commit on `claude/client-api-doc-review-Rz70X` updating
`docs/client-api.md` (and only that file) with:

- Corrected subject strings, field names/types, error messages.
- New entries for undocumented handlers / fields / error paths /
  triggered events.
- Removed entries for things documented but not in code.
- Updated "Triggered events" lists per RPC to match the publishes the
  workers actually perform.

No code changes. No restructuring beyond what's needed to land the
corrections.

## Risks & mitigations

| Risk | Mitigation |
|------|------------|
| Agent reports contradict each other (e.g., gatekeeper says event X comes from broadcast-worker; broadcast-worker doesn't publish X). | Aggregator (me) resolves by re-reading the cited code; if still ambiguous, I raise it to the user before editing. |
| Single-file edit overwrites concurrent work. | Branch is dedicated to this review pass; working tree confirmed clean before edits. |
| Agent misses an undocumented handler. | Every RPC agent is instructed to grep for `nc.QueueSubscribe` / `natsrouter.Register` in its service and reconcile against the doc, not just walk the doc top-down. |
| Doc accuracy regresses after merge as services evolve. | Out of scope for this pass. CLAUDE.md already requires PRs touching client-facing handlers to update `client-api.md` in the same PR; reinforcing that is a separate concern. |

## Success criteria

- Every RPC in `client-api.md` §2.2, §3, §4 has its subject, request schema,
  response schema, error envelope, and triggered events verified against
  current code.
- Every client-visible publish by `broadcast-worker`, `notification-worker`,
  `room-worker`, and `inbox-worker` is reflected somewhere in
  `client-api.md` (either under the triggering RPC's "Triggered events" or
  under §5 Server-Pushed Events).
- User-approved diff committed to `claude/client-api-doc-review-Rz70X`.

---

## Revision 2 — scope expansion (2026-05-21)

After reviewing the consolidated changeset, the user expanded scope from
**doc-only** to **code + doc**, restructured the doc, and made these
decisions. This revision supersedes the "No code changes" statements above.

### A/B/C decisions

- **A (List Rooms / Get Room membership filter):** Remove BOTH `rooms.list`
  and `rooms.get` client RPCs from the codebase and the doc. `rooms.list`
  is unused by the frontend; `rooms.get` is used in two live frontend paths
  (`MessageActionMenu` recipient count, `useRoomSubscriptions` metadata
  fetch), so the frontend is rewired off `getRoom` as part of this work.
  `store.GetRoom` stays — four other handlers depend on it internally.
- **B (sanitizeError allow-list):** Fix the code, not the doc. Add the
  remove-member validation errors and the list-members `limit`/`offset`
  errors to the room-service sanitizer allow-list so they reach the wire
  verbatim, then document them.
- **C (edit/delete zero-valued base fields):** Fix the code. Introduce
  dedicated flattened event structs for message-edited and message-deleted
  in `broadcast-worker` (only the fields a client needs) so the wire payload
  no longer carries zero-valued `RoomEvent` base fields. Document the new
  shape.

### Code changes in scope (each TDD: Red → Green → Refactor → commit)

1. **room-service** — remove `natsListRooms` + `natsGetRoom` handlers, their
   `RoomCreateWildcard`-style subscriptions, `store.ListRooms`,
   `model.ListRoomsResponse`, and the `RoomsList*`/`RoomsGet*` subject
   builders. Keep `store.GetRoom` (used by member add/remove/role handlers).
2. **room-service** — add remove-member errors (`exactly one of account or
   orgId must be set`, `cannot remove the last member of the room`, `last
   owner cannot leave the room`, `org members cannot leave individually`,
   `room ID mismatch`, `remove-member only supported on channel rooms`) and
   list-members errors (`limit must be > 0`, `offset must be >= 0`) to the
   `sanitizeError` allow-list. Prefer sentinel + `errors.Is` over substring
   matching per CLAUDE.md.
3. **message-gatekeeper** — add a `requestId` validation check, then run the
   `/simplify` command on the touched code. Update the doc to match the new
   behavior.
4. **broadcast-worker + pkg/model** — define `EditRoomEvent` /
   `DeleteRoomEvent` (flattened, only needed fields), publish those instead
   of the generic `RoomEvent` for mutation fan-out.
5. **search-service** — add `offset` and `limit` to the search-users request
   (`SearchUsersRequest` + forward to the HR client), documented with an
   example.
6. **chat-frontend** — remove `getRoom` usage in `MessageActionMenu` and
   `useRoomSubscriptions`; delete the `getRoom` api module + `roomsGet` /
   `roomsList` subject builders.

### Doc restructure (beyond field/error/event corrections)

- **Remove** all backend-internal / cross-site / federation content — the
  frontend never observes it.
- **Remove** the Dev mode subsection from `POST /auth`.
- **Remove** server-to-server RPCs (`chat.server.>`) entirely.
- **Remove** the standalone Room schema; document each RPC's actual handler
  response shape, verified against code.
- **Remove** the List Rooms / Get Room sections.
- **Restructure §5:** delete the standalone "Server-Pushed Events" section;
  move each `RoomKeyEvent` into the triggering RPC's "Triggered events"
  (Create Room, Add Members, Remove Member). Keep a dedicated **Room
  Encryption** section that explains *client decryption behavior* only.
- **Message-send fan-out:** the client receives broadcast-worker's
  `publishChannelEvent` / `publishDMEvents` output; the message JSON is built
  by broadcast-worker's `buildClientMessage`. Verify and document the real
  payload + example. Drop the notification-worker event (not part of the
  current design).

### Verification mandate

Before documenting any RPC response, read the handler and its response
struct to confirm the exact wire shape (subjects, field names, types,
optionality). Do not document from the doc's prior claims.

### Updated risks

| Risk | Mitigation |
|------|------------|
| Removing `rooms.get` breaks the live frontend. | Rewire the two frontend call sites and run the frontend test suite before committing. |
| Code changes break CI (lint, tests, SAST). | Run `make lint` + `make test SERVICE=<svc>` after each change; TDD each one. |
| `/simplify` alters behavior of the requestId check. | Re-run gatekeeper unit tests after `/simplify`; confirm the validation still holds. |
| Flattened edit/delete structs break existing consumers (frontend decoder, tests). | Grep for consumers of `messageEdited`/`messageDeleted` payloads; update or confirm none depend on the base fields. |
