# Client API Schema Documentation Normalization — Design

**Date:** 2026-06-08
**Branch:** `claude/client-api-schema-docs-x60vtx`
**Scope:** `docs/client-api.md` + a new rule in `CLAUDE.md`

## Problem

`docs/client-api.md` documents every client-facing RPC, but the schema
documentation is inconsistent:

- ~36 fields are typed `object`, which tells a frontend developer nothing
  about the actual shape. Many of these have nested children described only
  in prose (e.g. `{ engName, chineseName }`).
- Some RPCs (`Search Apps`, `Search Users`) define no response field table
  at all — they show a JSON blob and defer to Go structs.
- Some success responses lack a JSON example.
- The search-service section uses a different heading style
  (`**Request body:**`) than the rest of the doc (`##### Request body`).

## Goals

1. Every request body and response payload is a field table with an
   explicit type for each field. No field is typed `object`.
2. Compound types are named and defined in their own table; fields reference
   them by linked name (e.g. `[Participant](#participant)`, `ChannelRef[]`).
3. Every success response has a JSON example.
4. A `CLAUDE.md` rule enforces this for all future edits.

Non-goals: rewriting error prose, triggered-events sections, or any wording
beyond what is needed to fix schemas. Keep the diff focused.

## Approach

### Shared schemas section (`### 3.0 Shared schemas`)

A new subsection at the top of §3 holds one field-table per reusable type.
Every RPC links to these instead of repeating them.

| Type | Fields (summary) |
|---|---|
| `Participant` | `userId`, `account`, `siteId?`, `engName?`, `chineseName?` |
| `UserRef` | `id`, `account`, `isBot?` — lean removed/mention user |
| `ChannelRef` | `roomId`, `siteId` |
| `EncryptedMessage` | `version`, `nonce`, `ciphertext` |
| `AsyncJobResult` | moved here (shared by Create Room / Add Members / Remove Member) |
| `HrInfo` | `engName`, `chineseName` |

The large `Message` schema stays in §3.2 (already the canonical
`#message-schema` anchor). Its sub-objects — `file`, `card`, `cardAction`,
`quotedParentMessage`, `reactions`, `sender` — get promoted from inline
`object` prose to their own named tables placed under the Message schema.

### Type-column convention

- Replace `object` with the concrete type name as a markdown link:
  `[Participant](#participant)`, `[Message](#message-schema)`.
- Arrays: `Participant[]`, `ChannelRef[]`.
- Maps: `map<emoji, UserRef[]>` with the value type linked.
- One-off anonymous objects get a small named sub-table directly under the
  RPC that uses them (e.g. `meta`, `reactionDelta`, `CmdBlock`, app-tab
  structures).

### Per-RPC sweep

Cover every RPC: §2 `POST /auth`; §3.1 room-service (18); §3.2
history-service (14); §3.3 search-service (4); §4 Send Message; §5 Room
Encryption RPCs. For each: ensure a request-body table, a response field
table with explicit types, and a success JSON example.

Specific gaps to close:
- `Search Apps` / `Search Users`: add explicit `SearchApp` / `SearchUser`
  tables plus nested `assistant` and `sponsors` tables.
- Normalize search-service heading style to match the rest of the doc.
- Add JSON examples wherever a success response is missing one.

### CLAUDE.md rule

New concise subsection under **Section 5: Workflow Guardrails**, beside the
existing client-api rule (cross-reference, do not duplicate):

- Any change to a client-facing RPC (NATS subject prefixed `chat.user.`)
  must update `docs/client-api.md` in the same PR.
- Every request body and response payload is a field table; every field has
  an explicit type — never `object`. Compound types get their own named
  table (shared in §3.0, one-offs inline) and are referenced by linked name.
- Every success response includes a JSON example.
- Keep edits clean: minimal prose, no redundant commentary.

## Verification

- `grep -n '\bobject\b' docs/client-api.md` returns only legitimate prose
  uses (e.g. "the room object is never returned"), no type-column `object`.
- Every `##### Success response` block is followed by both a field table and
  a fenced ```json``` example.
- Markdown anchor links resolve (no dangling `#...` references).
