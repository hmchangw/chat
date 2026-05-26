# Coverage Register

The catalog of **documented behavior cases** the suite is expected to
cover. Each `## <slug>` is one case, transcribed from a design source
(with citation). A scenario claims a case with a `@covers:<slug>` tag.

`make integration-suite-coverage` (and every run's `last-run.md`) scores:

- **covered** — a scenario tagged `@covers:<slug>` passed
- **known-gap** — `Status: blindspot` (a recorded, accepted gap; see
  `blindspots.md`)
- **uncovered** — documented, but no passing scenario and not a known gap

Report-only: nothing fails on low coverage. Seeded from the room /
history / messaging contract extractions; trim or extend as scenarios
land. `Status:` is declared intent — actual covered/uncovered is
computed from the run.

<!-- Entry template:

## <slug>

**Service:** <service>
**Source:** docs/superpowers/specs/<file>.md § <section>
**Behavior:** <one line, user-visible>
**Status:** covered | blindspot | todo

-->

## room-create-channel

**Service:** room
**Source:** room-service/handler.go::handleCreateRoom (NATS rooms.create)
**Behavior:** An authenticated user creates a channel room and gets the Room back.
**Status:** covered

## room-roleupdate-invalid-role

**Service:** room
**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #3
**Behavior:** newRole not owner/member is rejected ("invalid role: must be owner or member").
**Status:** covered

## room-roleupdate-invalid-subject

**Service:** room
**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #1
**Behavior:** A malformed role-update subject is rejected.
**Status:** todo

## room-roleupdate-invalid-request

**Service:** room
**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #2
**Behavior:** An unparseable UpdateRoleRequest payload is rejected.
**Status:** todo

## room-roleupdate-not-channel

**Service:** room
**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #4
**Behavior:** Role update on a non-channel room is rejected.
**Status:** todo

## room-roleupdate-only-owners

**Service:** room
**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #5
**Behavior:** A non-owner requester cannot update roles.
**Status:** todo

## room-roleupdate-target-not-member

**Service:** room
**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #6
**Behavior:** Updating the role of a non-member is rejected.
**Status:** todo

## room-roleupdate-already-owner

**Service:** room
**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #7
**Behavior:** Promoting a user who is already an owner is rejected.
**Status:** todo

## room-roleupdate-last-owner

**Service:** room
**Source:** docs/superpowers/specs/2026-04-13-role-update-design.md § Validation Rules (room-service) #8
**Behavior:** The last owner cannot self-demote.
**Status:** todo

## history-thread-empty-id

**Service:** history
**Source:** docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md § Error Matrix
**Behavior:** Empty threadMessageId is rejected as bad_request.
**Status:** covered

## history-thread-not-subscribed

**Service:** history
**Source:** docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md § Error Matrix
**Behavior:** A caller not subscribed to the room is forbidden.
**Status:** blindspot

## history-thread-parent-is-reply

**Service:** history
**Source:** docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md § Error Matrix
**Behavior:** threadMessageId must be a top-level message, not a reply.
**Status:** todo

## history-thread-invalid-cursor

**Service:** history
**Source:** docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md § Error Matrix
**Behavior:** An invalid pagination cursor is rejected as bad_request.
**Status:** todo

## history-threadparent-empty

**Service:** history
**Source:** docs/superpowers/specs/2026-04-23-history-list-threads-redesign.md § 3.3
**Behavior:** Subscribed caller with zero matches gets {parentMessages:[], total:0}.
**Status:** todo

## history-threadparent-invalid-filter

**Service:** history
**Source:** docs/superpowers/specs/2026-04-23-history-list-threads-redesign.md § 3.3
**Behavior:** An invalid thread filter is rejected as bad_request.
**Status:** todo

## history-threadparent-not-subscribed

**Service:** history
**Source:** docs/superpowers/specs/2026-04-23-history-list-threads-redesign.md § 3.3
**Behavior:** A caller not subscribed to the room is forbidden.
**Status:** todo

## messaging-submit-empty-content

**Service:** messaging
**Source:** docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md § Error Handling
**Behavior:** A message with empty content is rejected.
**Status:** blindspot

## messaging-submit-invalid-id

**Service:** messaging
**Source:** docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md § Error Handling
**Behavior:** A message with an invalid message ID is rejected.
**Status:** blindspot

## messaging-submit-thread-fields-unpaired

**Service:** messaging
**Source:** message-gatekeeper/handler.go (validateThreadParentFields; spec-silent)
**Behavior:** threadParentMessageId without threadParentMessageCreatedAt is rejected.
**Status:** todo
