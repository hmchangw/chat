# Client API Schema Documentation Normalization — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every RPC in `docs/client-api.md` document its request body and success response as field tables with explicit types (no `object`) plus a JSON example, and add a CLAUDE.md rule enforcing it.

**Architecture:** Define reusable types once in a new `### 3.0 Shared schemas` section; every RPC links to them by name. Promote inline `object` prose to named tables. One-off objects get a small local sub-table. This is a documentation-only change — verification is by `grep`, not unit tests.

**Tech Stack:** Markdown (GitHub-flavored). Anchors are GitHub auto-slugs (lowercase, spaces→hyphens, punctuation stripped).

---

## File Structure

- Modify: `docs/client-api.md` — all schema edits.
- Modify: `CLAUDE.md` — one new subsection under Section 5.

Anchor convention for links: a heading `#### Participant` → `#participant`; `#### Message schema` → `#message-schema` (already used).

---

### Task 1: Add `### 3.0 Shared schemas` section

**Files:**
- Modify: `docs/client-api.md` — insert immediately after the `## 3. Request/Reply Methods` intro (before `### 3.1 room-service` at line ~219).

- [ ] **Step 1: Read the §3 intro region**

Read `docs/client-api.md:217-240` to find the exact insertion point and surrounding style.

- [ ] **Step 2: Insert the shared-schemas section**

Add `### 3.0 Shared schemas` with one `####` sub-heading + field table per type. Each table uses columns `| Field | Type | Notes |` (match existing style). Define:

- **Participant** — `userId` string, `account` string, `siteId` string (optional), `engName` string (optional), `chineseName` string (optional). Note: actor/sender identity used across room and message events; optional fields present per context.
- **UserRef** — `id` string, `account` string, `isBot` boolean (optional). Note: lean user reference (removed-member refs, mention rows). On org removals only `account` is guaranteed.
- **ChannelRef** — `roomId` string, `siteId` string. Note: reference to another channel whose members are copied/added.
- **EncryptedMessage** — `version` integer, `nonce` string (base64), `ciphertext` string (base64). Note: room ciphertext envelope (`roomcrypto.EncryptedMessage`); see §5.
- **HrInfo** — `engName` string, `chineseName` string. Note: HR display names.
- **AsyncJobResult** — move the existing table from under Add Members (currently `docs/client-api.md:386-399`) here verbatim; keep the `###### AsyncJobResult` content but re-home it as `#### AsyncJobResult`. Note: shared by Create Room, Add Members, Remove Member.

- [ ] **Step 3: Re-point the moved AsyncJobResult references**

Update the three references that read "See the [AsyncJobResult schema](#asyncjobresult) under Add Members" (and the Remove Member equivalent) to drop "under Add Members" and link `#asyncjobresult`. Find them: `grep -n 'AsyncJobResult' docs/client-api.md`.

- [ ] **Step 4: Verify links + commit**

Run: `grep -nE '#(participant|userref|channelref|encryptedmessage|hrinfo|asyncjobresult)' docs/client-api.md`
Expected: shows the new anchors are referenced; no "under Add Members" text remains.

```bash
git add docs/client-api.md
git commit -m "docs(client-api): add shared schemas section"
```

---

### Task 2: Promote Message sub-object schemas (§3.2)

**Files:**
- Modify: `docs/client-api.md` — the Message schema region (`#### Message schema`, ~1469-1550).

- [ ] **Step 1: Read the Message schema region**

Read `docs/client-api.md:1469-1551`.

- [ ] **Step 2: Replace `object` cells with linked named types and add sub-tables**

In the Message field table, change the Type cell of each compound field from `object` to a linked type name, and add a `#####` sub-table for each directly beneath the Message schema:

- `sender` → `[Participant](#participant)` (no new table; reuse shared).
- `file` → `[MessageFile](#messagefile)` — table: `id` string, `name` string, `type` string.
- `card` → `[MessageCard](#messagecard)` — table: `template` string, `data` string (base64, optional).
- `cardAction` → `[MessageCardAction](#messagecardaction)` — table: `verb` string, `text` string (optional), `cardId` string (optional), `displayText` string (optional), `hideExecLog` boolean (optional), `cardTmId` string (optional), `data` string (optional).
- `quotedParentMessage` → `[QuotedParentMessage](#quotedparentmessage)` — keep/convert the existing embedded-snapshot table; ensure its own `object` fields are typed.
- `reactions` → `map<emoji, UserRef[]>` (link UserRef). Keep the existing FIFO-ordering note.
- `pinnedBy` → `[Participant](#participant)`.

- [ ] **Step 3: Verify + commit**

Run: `sed -n '1469,1560p' docs/client-api.md | grep -c '| object'`
Expected: `0`

```bash
git add docs/client-api.md
git commit -m "docs(client-api): type Message sub-objects explicitly"
```

---

### Task 3: room-service RPC sweep (§3.1)

**Files:**
- Modify: `docs/client-api.md` — `### 3.1 room-service` region (~219-1424).

- [ ] **Step 1: List remaining object cells in range**

Run: `awk 'NR>=219 && NR<=1424 && /\| object/' docs/client-api.md`
Note each field; these are the targets.

- [ ] **Step 2: Replace each, reading the RPC first**

For each hit, read its RPC block, then replace the `object` Type cell with a linked named type. Mapping (known from survey):
- `subscription` (add/role/remove/mute/favorite) → `[Subscription](#subscription)` for full records; the Remove-Member lean ref → a `RemovedMemberRef` sub-table (`roomId` string, `roomType` string, `u` [UserRef](#userref)).
- `subscription.u` → `[UserRef](#userref)`.
- `member` (List Members) → `[RoomMemberEntry](#roommemberentry)`; ensure that entry has its own field table.
- `hrInfo` → `[HrInfo](#hrinfo)`.
- `app` (mentionable / app tabs) → a named `MentionApp` / `RoomAppTab` sub-table with `name` string and `assistant` `[AppAssistant](#appassistant)`.
- `assistant` → `[AppAssistant](#appassistant)` sub-table (`name` string, plus fields present; mark optional).
- `cmdBlocks` → `CmdBlock[]`; add a recursive `CmdBlock` sub-table (`blocks` `CmdBlock[]` optional, `modal` `CmdBlockModal` optional with `command` string + `param` string).
- Any `Subscription` reference must resolve: if no canonical Subscription table exists, add one under §3.0 or §3.1 and link it.

- [ ] **Step 3: Verify range clean + commit**

Run: `awk 'NR>=219 && NR<=1424 && /\| object/' docs/client-api.md | wc -l`
Expected: `0`

```bash
git add docs/client-api.md
git commit -m "docs(client-api): type room-service request/response objects"
```

---

### Task 4: history-service RPC sweep (§3.2)

**Files:**
- Modify: `docs/client-api.md` — `### 3.2 history-service` region (~1425-2471), excluding the Message schema already done in Task 2.

- [ ] **Step 1: List remaining object cells in range**

Run: `awk 'NR>=1425 && NR<=2471 && /\| object/' docs/client-api.md`

- [ ] **Step 2: Replace each, reading the RPC first**

Mapping (known from survey):
- `meta` → `[RoomMeta](#roommeta)` sub-table (`lastMsgAt` number optional, `createdAt` number optional; both UTC ms).
- `encryptedNewContent` → `[EncryptedMessage](#encryptedmessage)`.
- `messagePinned.pinnedBy` / `messageUnpinned.unpinnedBy` → `[Participant](#participant)`.
- `message` (pin/unpin/react event payloads) → `[Message](#message-schema)`.
- `actor` (react toggle) → `[Participant](#participant)`.
- `reactionDelta` → `[ReactionDelta](#reactiondelta)` sub-table (`shortcode` string, `action` string, `actor` [Participant](#participant)).

- [ ] **Step 3: Verify range clean + commit**

Run: `awk 'NR>=1425 && NR<=2471 && /\| object/' docs/client-api.md | wc -l`
Expected: `0`

```bash
git add docs/client-api.md
git commit -m "docs(client-api): type history-service request/response objects"
```

---

### Task 5: search-service sweep — tables + heading normalization (§3.3)

**Files:**
- Modify: `docs/client-api.md` — `### 3.3 search-service` region (~2472-2744).

- [ ] **Step 1: Read the whole search-service section**

Read `docs/client-api.md:2472-2744`.

- [ ] **Step 2: Normalize heading style**

Convert `**Request body:**` → `##### Request body`, `**Response body:**` / `**Response body — …:**` → `##### Success response`, `**Errors:**` → `##### Error response`, for Search Apps and Search Users, matching the §3.1/§3.2 style.

- [ ] **Step 3: Add the missing response field tables**

- **Search Apps** `SearchApp`: `id` string, `name` string, `description` string, `assistant` `[AppAssistant](#appassistant)`, `sponsors` `Sponsor[]`. Add `Sponsor` sub-table (`name` string, `phone` string). Reuse the `AppAssistant` table from Task 3 (`enabled` boolean, `name` string, `settingsUrl` string) — if Task 3's AppAssistant lacks `enabled`/`settingsUrl`, extend it and mark fields optional per context.
- **Search Users** `SearchUser`: replace the "see `pkg/model.SearchUser`" deferral with an explicit table — `account` string, `engName` string, `chineseName` string (the documented subset), with a note that additional legacy fields mirror `GET /api/v3/users`.
- Wrap each response payload field in a top-level table too (e.g. Search Apps response `apps` → `SearchApp[]`).

- [ ] **Step 4: Verify + commit**

Run: `awk 'NR>=2472 && NR<=2744 && /\| object/' docs/client-api.md | wc -l` → `0`
Run: `grep -nE 'SearchApp|SearchUser|AppAssistant|Sponsor' docs/client-api.md` → tables present.

```bash
git add docs/client-api.md
git commit -m "docs(client-api): add search-service field tables and normalize headings"
```

---

### Task 6: auth / send / encryption sweep (§2, §4, §5)

**Files:**
- Modify: `docs/client-api.md` — `## 2` (~134-216), `## 4` (~2746-2960), `## 5` (~2964-3072).

- [ ] **Step 1: List remaining object cells in these ranges**

Run: `awk '(NR>=134 && NR<=216) || (NR>=2746 && NR<=3072) && /\| object/' docs/client-api.md`

- [ ] **Step 2: Replace each, reading the block first**

Mapping (known from survey):
- Send Message `quotedParentMessage` → `[QuotedParentMessage](#quotedparentmessage)`.
- Send event `message` → `[ClientMessage](#message-schema)` (note: Message + `sender` Participant); `encryptedMessage` → `[EncryptedMessage](#encryptedmessage)`.
- §5 `RoomKeyEvent`, `RoomKeyGetRequest`, `RoomKeyGetResponse` payloads: ensure each has a field table with explicit types and a JSON example; replace any `object` cell.
- §2 POST /auth request/response: confirm tables are explicit (already mostly are); fix any `object`.

- [ ] **Step 3: Verify ranges clean + commit**

Run: `awk '(NR>=134 && NR<=216) || (NR>=2746 && NR<=3072){ if(/\| object/) c++ } END{print c+0}' docs/client-api.md`
Expected: `0`

```bash
git add docs/client-api.md
git commit -m "docs(client-api): type auth, send, and encryption payloads"
```

---

### Task 7: Add CLAUDE.md rule

**Files:**
- Modify: `CLAUDE.md` — Section 5: Workflow Guardrails, beside the existing client-api.md bullet under "Before Committing".

- [ ] **Step 1: Read the surrounding guardrails**

Read the `### Before Committing` region of `CLAUDE.md` (the bullet starting "If your changes touch a client-facing handler").

- [ ] **Step 2: Add a concise subsection**

Insert after the "Before Committing" list:

```markdown
### Documenting the Client API (`docs/client-api.md`)
- Any change to a client-facing RPC (a handler whose NATS subject begins with `chat.user.`) must be reflected in `docs/client-api.md` in the same PR (see the client-facing-handler bullet above).
- Every request body and response payload is a field table (current style). Each field has an explicit type — never `object`. Compound types get their own named table (shared types in §3.0 Shared schemas, one-offs inline) and are referenced by linked name.
- Every success response includes a JSON example.
- Keep edits clean: minimal prose, no redundant comments or long explanations.
```

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: require explicit schema tables for client-api.md edits"
```

---

### Task 8: Final verification

- [ ] **Step 1: No type-cell `object` remains**

Run: `grep -nE '^\| *`[^|]*` *\| *object' docs/client-api.md`
Expected: no output. (Prose uses like "the room object is never returned" are fine and stay.)

- [ ] **Step 2: Every success response has a JSON example**

Run:
```bash
awk '/^#{4,5} Success response/{s=NR;h=$0;j=0} s&&/^```json/{j=1} s&&/^#{1,5} /&&NR>s{if(!j)print h" @"s; s=0} END{if(s&&!j)print h" @"s}' docs/client-api.md
```
Expected: no output.

- [ ] **Step 3: No dangling anchor links**

Run: `grep -oE '\]\(#[a-z0-9-]+\)' docs/client-api.md | sort -u` and spot-check each target heading exists.
Expected: each `#anchor` maps to a real heading.

- [ ] **Step 4: Update PR checklist + push**

Edit the PR body checklist to mark implementation done; push the branch.

```bash
git push -u origin claude/client-api-schema-docs-x60vtx
```

---

## Self-Review Notes

- **Spec coverage:** Tasks 1–2 cover shared schemas + Message sub-objects; Tasks 3–6 cover the per-RPC sweep across all sections; Task 7 covers CLAUDE.md; Task 8 verifies goals 1–3. All four spec goals map to tasks.
- **Type consistency:** `AppAssistant` is introduced in Task 3 and reused in Task 5 — Task 5 Step 3 explicitly extends it if needed rather than redefining. `QuotedParentMessage` is named in Task 2 and reused in Task 6. `UserRef`, `Participant`, `EncryptedMessage` defined in Task 1 and linked everywhere.
- **Note:** Final table contents are produced during execution after reading each section; the field lists above come from the completed survey and are the authoritative targets.
