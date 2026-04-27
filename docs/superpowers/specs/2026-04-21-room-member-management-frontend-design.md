# Room Member Management Frontend Design

**Date:** 2026-04-21
**Status:** Draft
**Scope:** `chat-frontend/` only — no backend changes.
**Depends on (already implemented):**
- `2026-04-14-add-member-design.md`
- `2026-04-14-remove-member-design.md`
- `2026-04-13-role-update-design.md`

## Summary

Expose the existing room member management backend operations (add, remove, remove-org, role-update, self-leave) in the React dev-tool frontend. The UI is a single tabbed "Manage Members" dialog opened from the room header, plus a separate "Leave" button for self-leave. No new backend endpoints. No member-list view. Users type account/org/channel identifiers directly — consistent with the existing `CreateRoomDialog` style.

## Scope

### In scope
- New subject builders in `src/lib/subjects.js` for `member.add`, `member.remove`, `member.role-update`.
- `ManageMembersDialog` component with four tabs: Add, Remove, Role, Remove Org.
- `LeaveRoomButton` component in the room header.
- History-mode checkbox in Add form.
- Unit tests for every new component + the new subject builders.
- Wiring in `ChatPage` to show "Members" and "Leave" buttons for `group` rooms only.

### Out of scope
- Member list / roster view (no backend endpoint today — deferred).
- Subscribing to `chat.room.{roomID}.event.member` (no UI consumes it).
- Tracking own roles per room in frontend state (server rejects with error banner instead).
- Picker UI for accounts/orgs/channels (plain text inputs, matching `CreateRoomDialog`).
- Integration tests (backend contract covered by `room-service` / `room-worker` integration tests).

## Architecture

```
ChatPage header
  ├─ "Members" button  ──> opens ManageMembersDialog
  └─ "Leave" button    ──> window.confirm → memberRemove(self)

ManageMembersDialog (tab strip)
  ├─ Add          ── AddMembersForm      ── memberAdd(...)
  ├─ Remove       ── RemoveMemberForm    ── memberRemove({account})
  ├─ Role         ── RoleUpdateForm      ── memberRoleUpdate(...)
  └─ Remove Org   ── RemoveOrgForm       ── memberRemove({orgId})
```

All four forms use `NatsContext.request(subject, payload)`. On success (`{status: "accepted"}`) the form shows a transient confirmation and clears its inputs. On error, the form renders `parsed.error` in a `.dialog-error` banner.

`ManageMembersDialog` and `LeaveRoomButton` are only rendered when `selectedRoom?.type === 'group'`. DMs hide both — the backend rejects member management on DMs, and there's no sensible leave flow for a 2-person DM.

No changes to `NatsContext`, `RoomEventsContext`, or the reducer. No new subscriptions. Existing `subscription.update` and `roomMetadataUpdate` subscriptions already deliver all live effects visible to the current user (own room added/removed, `userCount` updated).

## File Layout

```
chat-frontend/src/
  components/
    ManageMembersDialog.jsx              # new — dialog shell + tab strip
    ManageMembersDialog.test.jsx         # new
    LeaveRoomButton.jsx                  # new
    LeaveRoomButton.test.jsx             # new
    manageMembers/                       # new subdirectory
      AddMembersForm.jsx
      AddMembersForm.test.jsx
      RemoveMemberForm.jsx
      RemoveMemberForm.test.jsx
      RoleUpdateForm.jsx
      RoleUpdateForm.test.jsx
      RemoveOrgForm.jsx
      RemoveOrgForm.test.jsx
  lib/
    subjects.js                          # modified — add 3 builders
    subjects.test.js                     # modified — add 3 cases
  pages/
    ChatPage.jsx                         # modified — header buttons
    ChatPage.test.jsx                    # new — covers header button visibility
```

## Subject Builders (new)

Added to `chat-frontend/src/lib/subjects.js`, mirroring `pkg/subject/subject.go`:

```js
export function memberAdd(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.member.add`
}

export function memberRemove(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.member.remove`
}

export function memberRoleUpdate(account, roomId, siteId) {
  return `chat.user.${account}.request.room.${roomId}.${siteId}.member.role-update`
}
```

- `account` is the **requester's** account (`user.account` from `NatsContext`).
- `siteId` is the **room's** site (`room.siteId` from the summary object), not the user's site. NATS gateways route cross-site requests transparently.

`subjects.test.js` gains one `it` per builder verifying the exact produced string.

## Payloads

Payload shapes mirror the Go request structs from the approved backend specs.

### Add — `memberAdd(account, roomId, siteId)`

```json
{
  "roomId": "r1",
  "users": ["alice", "bob"],
  "orgs": ["eng-frontend"],
  "channels": ["r-existing"],
  "history": { "mode": "all" }
}
```

- Any combination of `users` / `orgs` / `channels` is allowed; at least one must be non-empty (validated client-side to disable the submit button).
- `history.mode`: `"all"` when the "Share room history" checkbox is checked (default), `"none"` when unchecked.

### Remove individual — `memberRemove(account, roomId, siteId)`

```json
{ "roomId": "r1", "account": "bob" }
```

### Remove org — `memberRemove(account, roomId, siteId)`

```json
{ "roomId": "r1", "orgId": "eng-frontend" }
```

Exactly one of `account` or `orgId` is set per request — the two forms are in separate tabs and build distinct payloads.

### Role update — `memberRoleUpdate(account, roomId, siteId)`

```json
{ "roomId": "r1", "account": "bob", "newRole": "owner" }
```

`newRole` is either `"owner"` (promote) or `"member"` (demote).

### Self-leave — `memberRemove(account, roomId, siteId)` with self

```json
{ "roomId": "r1", "account": "<user.account>" }
```

Same subject and payload shape as individual remove; backend distinguishes self-leave via `req.Requester == req.Account`.

## Components

### `ManageMembersDialog`

Props: `{ room, onClose }`.

State:
- `mode`: one of `'add' | 'remove' | 'role' | 'removeOrg'`. Default `'add'`.

Renders the existing `.dialog-overlay` + `.dialog` shell (same CSS classes as `CreateRoomDialog`), a tab strip, and the active sub-form. Each tab button sets `mode`. Sub-forms receive only `{ room }`; the dialog stays open after a successful operation so users can perform multiple mutations in one session — closing is the user's responsibility via the dialog's Close button or overlay click.

### Sub-forms — common shape

Each sub-form is a standalone component owning its own local state:

```js
const [loading, setLoading] = useState(false)
const [error, setError] = useState(null)
const [success, setSuccess] = useState(false)
```

On submit:
1. Clear `error` and `success`, set `loading = true`.
2. Call `request(subject, payload)`; on throw, set `error = err.message` and return.
3. On success, clear inputs, set `success = true`, start a 3-second timer that clears `success`.
4. Always set `loading = false` in `finally`.

The `success` flag renders a small "Accepted" label near the submit button. The `error` string renders in the existing `.dialog-error` banner.

The form disables all inputs and the submit button while `loading`.

### `AddMembersForm`

Inputs:
- `accounts` — comma-separated text input (`"alice, bob"`)
- `orgs` — comma-separated text input
- `channels` — comma-separated text input (room IDs)
- `shareHistory` — checkbox, default checked

On submit, parse each comma-separated field into `string[]` by splitting on `,`, trimming, and filtering empties (same helper style as `CreateRoomDialog:23-27`). Submit button is disabled unless at least one of the three lists has ≥1 entry.

Payload: `{ roomId: room.id, users, orgs, channels, history: { mode: shareHistory ? 'all' : 'none' } }`.

### `RemoveMemberForm`

Single text input `account` + "Remove" button. Submit disabled when `account.trim()` is empty.

Payload: `{ roomId: room.id, account: account.trim() }`.

### `RoleUpdateForm`

Inputs:
- `account` — text input
- `newRole` — `<select>` with options `owner` and `member`, default `owner`

Submit disabled when `account.trim()` is empty.

Payload: `{ roomId: room.id, account: account.trim(), newRole }`.

### `RemoveOrgForm`

Single text input `orgId` + "Remove Org" button. Submit disabled when `orgId.trim()` is empty.

Payload: `{ roomId: room.id, orgId: orgId.trim() }`.

### `LeaveRoomButton`

Props: `{ room }`.

Renders a button labeled "Leave" in the chat header. On click:
1. `if (!window.confirm(\`Leave "${room.name}"?\`)) return`.
2. Call `request(memberRemove(user.account, room.id, room.siteId), { roomId: room.id, account: user.account })`.
3. On success, the existing `subscription.update` (action `"removed"`) subscription already dispatches `ROOM_REMOVED` in the reducer, which removes the room from the sidebar and clears the selection (handled by the existing `useEffect` in `ChatPage.jsx:16-21`). No manual UI update needed here.
4. On error, show a brief `alert()` with the error message. (Keeping the header button simple — no inline banner required; failures for self-leave are rare in practice and the dev tool's needs are modest.)

### `ChatPage` modifications

The chat header gains two new buttons next to "Logout", rendered only when `selectedRoom?.type === 'group'`:

```jsx
{selectedRoom?.type === 'group' && (
  <>
    <button onClick={() => setShowMembers(true)}>Members</button>
    <LeaveRoomButton room={selectedRoom} />
  </>
)}
```

`showMembers` state is local to `ChatPage`; when true, `<ManageMembersDialog room={selectedRoom} onClose={() => setShowMembers(false)} />` is rendered.

## Error Handling

- `NatsContext.request` already throws when the reply contains `parsed.error`. User-facing strings from `room-service` (e.g., `"only owners can add members to this room"`, `"cannot demote: you are the last owner"`, `"cannot add members to a DM room"`) pass through unchanged.
- Forms render `err.message` verbatim in `.dialog-error`.
- 5-second request timeout is inherited from `NatsContext` defaults. On timeout, the error banner shows the NATS timeout error — acceptable for a dev tool.
- No client-side retries; user re-submits.
- **Non-owners see every control.** When the server rejects (e.g., a non-owner tries to promote someone), the banner shows the server's error. This is explicitly chosen over hiding controls based on role.

## Events — Not Subscribed

The frontend does **not** subscribe to `chat.room.{roomID}.event.member`. Rationale:

| Effect | Already delivered via |
|--------|----------------------|
| Current user added to a room | `subscription.update` action `"added"` (existing) |
| Current user removed / left | `subscription.update` action `"removed"` (existing) |
| Current user's role changed | `subscription.update` action `"role_updated"` (existing; reducer ignores, no UI impact today) |
| Room `userCount` changed | `roomMetadataUpdate` (existing) |
| Another user's add/remove/role | Not displayed (no member list) |

Adding a subscription would be unused plumbing and would require an unsubscribe path in `RoomEventsContext`'s cleanup.

## Testing

Follows the existing Vitest + `@testing-library/react` patterns (`RoomList.test.jsx`, `MessageArea.test.jsx`, `CreateRoomDialog`). Each test mocks `useNats` and captures `request()`'s arguments.

| Test file | Coverage |
|-----------|----------|
| `subjects.test.js` | Existing file; add three cases covering `memberAdd` / `memberRemove` / `memberRoleUpdate` subjects match the Go builders exactly |
| `ManageMembersDialog.test.jsx` | Default tab is Add; tab switching renders correct sub-form; close button calls `onClose` |
| `AddMembersForm.test.jsx` | Submit disabled when all three inputs empty; parses comma-separated accounts/orgs/channels; history checkbox toggles `history.mode`; error banner on rejection; success clears inputs |
| `RemoveMemberForm.test.jsx` | Submit disabled when account empty; submits `{roomId, account}`; error banner on rejection; success clears input |
| `RoleUpdateForm.test.jsx` | Submits `{roomId, account, newRole}` for both `owner` and `member`; error banner; success clears account input |
| `RemoveOrgForm.test.jsx` | Submits `{roomId, orgId}`; error banner; success clears input |
| `LeaveRoomButton.test.jsx` | Renders nothing when room is a DM; shows confirm dialog; on confirm submits `{roomId, account: user.account}`; alert on error |
| `ChatPage.test.jsx` (new) | "Members" and "Leave" buttons hidden when no room selected; hidden on DM; visible on group room; Members button opens the dialog |

Coverage target: match the existing frontend style — meaningful unit tests per component, not a coverage percentage target.

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| No member list view (option B) | Avoids a new backend endpoint for v1; keeps PR scope to frontend only; matches dev-tool philosophy of typing identifiers directly |
| Tabbed single dialog (Approach 2) | Each sub-form stays small (~60-80 LOC); matches existing `.dialog` CSS; scales cleanly if a "Members" tab is added later |
| Separate "Leave" button (not a tab) | Self-leave is semantically distinct from managing others; backend emits a distinct `member_left` system message; button placement reflects that it acts on the current user |
| Always show controls; rely on server rejection (option b-i) | Dev-tool style; avoids plumbing per-room role tracking; server is the authority for authorization; error banners already carry sanitized messages |
| Expose history-mode toggle (option a-i) | Backend exposes the knob; exposing it in the dev tool lets us exercise both code paths; cost is one checkbox |
| Skip `chat.room.{roomID}.event.member` subscription | Existing `subscription.update` + `roomMetadataUpdate` already deliver every effect visible to the current user; subscribing would be unused |
| Comma-separated text inputs (no picker) | Matches `CreateRoomDialog`; pickers require a user-directory endpoint which doesn't exist |
| Redundant `roomId` in payload | Matches Go request structs exactly (backend binds from subject but accepts the field); easier to reason about wire format |
| Room-scoped `siteId` in subject | Backend routes by room's site; `room.siteId` is already on the summary object |
| No confirmation dialog for Remove/Remove Org | Dev tool; explicit account/org entry is already a de facto confirmation; `window.confirm` on Leave is enough to protect the most destructive action against misclicks |
| 3-second success flash + clear inputs | Immediate visual feedback without a modal; clearing inputs prevents accidental re-submits of the same payload |
| Plain `alert()` on Leave error | Leave lives outside the dialog; an alert is the cheapest way to surface errors there without a new banner component |

## Out-of-Scope / Future Work

- **Member roster view.** Requires a new backend endpoint (`rooms.members.{roomID}`) that aggregates `subscriptions` + `room_members`. Would fit naturally as a fifth tab. Deferred per clarifying question 2.
- **Account / org / channel pickers.** Would require a user/org directory endpoint. Not needed for dev tool.
- **Role-based control hiding.** Would require extending the summary/state to track own roles per room from `rooms.list`.
- **Subscribing to `event.member`** for live notifications ("Alice added Bob to this channel"). Useful if a roster view is added.
- **Restricted-room toggle UI.** `Room.Restricted` is a backend flag with no creation-path UI yet; out of scope here.
