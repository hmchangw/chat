# Architecture Gaps Report

Generated: 2026-05-21

This report cross-references three sources of truth:

| Source | Count |
|--------|-------|
| Slugs in `coverage.md` | 19 |
| Distinct `@covers:<slug>` tags across all feature files | 36 |
| Entries in `blindspots.md` | 33 |
| Distinct `@blindspot:<slug>` tags across all feature files | 33 |

---

## Section 1: Truly Uncovered Documented Behaviors

These are slugs that appear in `coverage.md` **and** have neither a `@covers:<slug>` tag in any `.feature` file nor an entry in `blindspots.md`. They represent architecture that is documented but not exercised and not acknowledged as a known gap. Each entry is an actionable authoring prompt: write a scenario, or promote it to a blindspot with a justification.

**Count: 13**

---

### room-roleupdate-invalid-subject

**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #1
**Description:** A malformed role-update subject is rejected.
**Category guess:** CAT-5 Field validation / subject-format guard
**Why uncovered:**
  - No scenario yet — author it. The role-update subject parser already exists in room-service; a NATS request with a syntactically broken subject token (e.g. too few dot-segments) should return a handler error. No primitives beyond standard NATS request/reply are needed — this is a Part-1 authorable scenario.

---

### room-roleupdate-invalid-request

**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #2
**Description:** An unparseable UpdateRoleRequest payload is rejected.
**Category guess:** CAT-5 Field validation / JSON parse guard
**Why uncovered:**
  - No scenario yet — author it. Send a non-JSON or structurally wrong body to the role-update subject and assert a bad_request class response. No Part-2 primitives needed.

---

### room-roleupdate-not-channel

**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #4
**Description:** Role update on a non-channel room is rejected.
**Category guess:** CAT-10 State preconditions
**Why uncovered:**
  - No scenario yet — author it. Requires a DM room to exist as a fixture, then attempt a role update. The DM-room fixture step is currently a blindspot (`room-add-member-to-dm`), but the room creation step itself works and returns a `roomType:"dm"` response. The suite can capture the returned `roomId` and issue the role-update against it. Unblocked in Part-1 if the DM-room fixture step is extended.

---

### room-roleupdate-only-owners

**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #5
**Description:** A non-owner requester cannot update roles.
**Category guess:** CAT-9 Authorization / requester-role guard
**Why uncovered:**
  - No scenario yet — author it. Create a channel room as alice (owner), invite bob as member, then have bob attempt a role update. The add-member pipeline step (`room-worker-add-member-persistence`) is a blindspot, but if room-service returns a member-exists membership check result, the guard can be tested by having a second user who was NOT added issue the request. The guard fires before any membership lookup on the requester — check the code path; it may be authorable with a user who simply lacks a subscription.

---

### room-roleupdate-target-not-member

**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #6
**Description:** Updating the role of a non-member is rejected.
**Category guess:** CAT-10 State preconditions / target membership check
**Why uncovered:**
  - No scenario yet — author it. Owner alice attempts to promote charlie who has never been added to the room. The store lookup returns "not found" and the handler rejects. No Part-2 primitives needed; the room and the owner subscription are set up by the room-create step already used in existing scenarios.

---

### room-roleupdate-already-owner

**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #7
**Description:** Promoting a user who is already an owner is rejected.
**Category guess:** CAT-10 State preconditions / duplicate-role guard
**Why uncovered:**
  - Code path probably absent from the suite's reachable paths. The guard fires only when the target already holds the `owner` role. Setting that state requires room-worker to have processed an add-member event (a pipeline step, blindspot `room-worker-add-member-persistence`) or a seed step. Writing the scenario will expose that the fixture is missing — tag `@blindspot:room-worker-already-owner-needs-mongo-seeding` when written.

---

### room-roleupdate-last-owner

**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #8
**Description:** The last owner cannot self-demote.
**Category guess:** CAT-10 State preconditions / last-owner guard
**Why uncovered:**
  - No scenario yet — author it. The room creator is the sole owner. Have alice (owner, creator) send a role-update demoting herself to member. The guard fires without any additional fixture. Authorable in Part-1 using only the room-create step already exercised in existing scenarios.

---

### history-thread-parent-is-reply

**Source:** docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md § Error Matrix
**Description:** threadMessageId must be a top-level message, not a reply.
**Category guess:** CAT-5 Field validation / reply-nesting guard
**Why uncovered:**
  - Code path exists but observation requires a Part-2 primitive — writing the scenario will likely produce a handler error or an empty result depending on how the guard is implemented. Verifying it requires a Cassandra row that has a non-empty `thread_parent_message_id` (i.e. it is itself a reply). That row requires either Cassandra seeding (Part-2) or a full message-pipeline run. Tag `@blindspot:history-thread-parent-is-reply-needs-cassandra-seeding` when written.

---

### history-thread-invalid-cursor

**Source:** docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md § Error Matrix
**Description:** An invalid pagination cursor is rejected as bad_request.
**Category guess:** CAT-5 Field validation / cursor format guard
**Why uncovered:**
  - No scenario yet — author it. Send a base64-corrupted or non-base64 cursor string in the `cursor` field and assert bad_request. No Cassandra fixture or Part-2 primitive needed; the cursor is validated before any Cassandra call.

---

### history-threadparent-empty

**Source:** docs/superpowers/specs/2026-04-23-history-list-threads-redesign.md § 3.3
**Description:** Subscribed caller with zero matches gets {parentMessages:[], total:0}.
**Category guess:** CAT-8 Empty / zero-result contract
**Why uncovered:**
  - Code path exists but observation requires a Part-2 primitive. A subscribed caller with no threads in their room produces the empty result, but establishing the subscription requires the room-worker add-member pipeline (blindspot `room-worker-add-member-persistence`). Tag `@blindspot:history-threadparent-empty-needs-subscription-fixture` when written, mirroring `history-empty-thread-requires-subscription-fixture`.

---

### history-threadparent-invalid-filter

**Source:** docs/superpowers/specs/2026-04-23-history-list-threads-redesign.md § 3.3
**Description:** An invalid thread filter is rejected as bad_request.
**Category guess:** CAT-5 Field validation / filter enum guard
**Why uncovered:**
  - No scenario yet — author it. Send an unknown `filter` value (e.g. `"bogus"`) to the list-threads handler and assert bad_request. Likely authorable in Part-1 without any subscription or Cassandra fixture, since validation fires before the store call.

---

### history-threadparent-not-subscribed

**Source:** docs/superpowers/specs/2026-04-23-history-list-threads-redesign.md § 3.3
**Description:** A caller not subscribed to the room is forbidden.
**Category guess:** CAT-9 Authorization / subscription guard
**Why uncovered:**
  - Code path probably absent — related blindspot `history-forbidden-class-unverifiable` already documents that the NATS classifier maps `code:"forbidden"` as `HandlerError` not `Auth`, so the scenario would be authorable but the assertion class would mismatch the spec. Write the scenario and tag `@blindspot:history-threadparent-not-subscribed-class-unverifiable` referencing the same classifier gap.

---

### messaging-submit-thread-fields-unpaired

**Source:** message-gatekeeper/handler.go (validateThreadParentFields; spec-silent)
**Description:** threadParentMessageId without threadParentMessageCreatedAt is rejected.
**Category guess:** CAT-5 Field validation / paired-fields guard
**Why uncovered:**
  - Code path exists but observation requires a Part-2 primitive. The gatekeeper validates thread fields during JetStream consumption (fire-and-forget), so the reply arrives on a decoupled response subject. The existing blindspot `message-submit-needs-jetstream-primitive` covers the same infrastructure gap for all gatekeeper validation cases. Tag `@blindspot:messaging-submit-thread-fields-unpaired-needs-jetstream-primitive` when written, or subsume under the existing `message-submit-needs-jetstream-primitive` blindspot.

---

## Section 2: Arch-Grounded Blindspots (Informational)

These slugs appear in both `coverage.md` (with `Status: blindspot`) and have a `@covers:<slug>` tag in a feature file (meaning a scenario exists but the behavior is accepted as a known gap). They are tracked correctly and require no action beyond the blindspot register.

**Count: 3**

- `history-thread-not-subscribed` — covered as known-gap; Source: docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md § Error Matrix; Blindspot reason: the `code:"forbidden"` response is classified as `HandlerError` not `Auth` by the NATS classifier (`history-forbidden-class-unverifiable`), so the scenario exists but the authorization-class assertion cannot be confirmed.
- `messaging-submit-empty-content` — covered as known-gap; Source: docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md § Error Handling; Blindspot reason: gatekeeper validation fires on a JetStream-consumed message; decoupled reply requires Part-2 JetStream primitive (`message-submit-needs-jetstream-primitive`).
- `messaging-submit-invalid-id` — covered as known-gap; Source: docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md § Error Handling; Blindspot reason: same as `messaging-submit-empty-content` — JetStream fire-and-forget, no Part-1 observable reply.

---

## Section 3: Orphan Registrations (Data Hygiene)

### Orphan @covers (tag in feature file, slug NOT in coverage.md)

These 30 slugs have `@covers:` tags in feature files but no matching entry in `coverage.md`. They represent scenarios that are exercising behaviors the coverage register does not yet catalog. Each should either be added to `coverage.md` or the tag removed.

| Orphan @covers slug | Likely service area |
|---------------------|---------------------|
| `history-delete-message-empty-id` | history |
| `history-delete-not-subscribed` | history |
| `history-edit-empty-content` | history |
| `history-edit-message-empty-message-id` | history |
| `history-edit-not-subscribed` | history |
| `history-get-message-empty-id` | history |
| `history-get-message-not-subscribed` | history |
| `history-list-threads-invalid-filter` | history |
| `history-list-threads-not-subscribed` | history |
| `history-load-history-not-subscribed` | history |
| `history-next-messages-invalid-cursor` | history |
| `history-next-messages-not-subscribed` | history |
| `history-surrounding-empty-message-id` | history |
| `history-surrounding-not-subscribed` | history |
| `messaging-delete-non-sender-forbidden` | messaging |
| `messaging-delete-non-subscriber-forbidden` | messaging |
| `messaging-edit-empty-content-rejected` | messaging |
| `messaging-edit-non-sender-forbidden` | messaging |
| `messaging-edit-non-subscriber-forbidden` | messaging |
| `messaging-submit-large-room-blocked` | messaging |
| `messaging-submit-large-room-thread-reply-exempt` | messaging |
| `messaging-submit-main-room-quoting-thread-reply` | messaging |
| `messaging-submit-not-subscribed` | messaging |
| `messaging-submit-oversized-content` | messaging |
| `messaging-submit-quote-parent-not-found` | messaging |
| `messaging-submit-quote-thread-mismatch` | messaging |
| `messaging-submit-siteid-mismatch-drop` | messaging |
| `messaging-submit-thread-parent-invalid-id` | messaging |
| `messaging-submit-thread-parent-missing-created-at` | messaging |
| `messaging-submit-valid` | messaging |

**Fix:** add a `## <slug>` entry to `coverage.md` for each, with `Source:`, `Behavior:`, and `Status:` fields.

### Orphan @blindspot (tag in feature file, slug NOT in blindspots.md)

None. All 33 `@blindspot:` tags in feature files have matching entries in `blindspots.md`.

### Orphan coverage.md entries (slug in coverage.md, no tag of any kind in any feature file)

All 13 entries in Section 1 above are also in this category — they have no `@covers:` and no `@blindspot:` tag. Listed there with full analysis; not repeated here.

### Orphan blindspots.md entries (slug in blindspots.md, no @blindspot tag in any feature file)

None. Every entry in `blindspots.md` has at least one `@blindspot:` tag in the feature files.

---

## Summary

| Metric | Count |
|--------|-------|
| Total slugs in coverage.md | 19 |
| Distinct `@covers:` slugs across feature files | 36 |
| Section 1 — truly uncovered documented behaviors | 13 |
| Section 2 — arch-grounded blindspots (known-gap, correctly tracked) | 3 |
| Section 3 — orphan `@covers` (feature tag, no coverage entry) | 30 |
| Section 3 — orphan `@blindspot` (feature tag, no blindspot entry) | 0 |
| Section 3 — orphan coverage.md entries (no feature tag of any kind) | 13 (= Section 1) |
| Section 3 — orphan blindspots.md entries (no feature tag) | 0 |
