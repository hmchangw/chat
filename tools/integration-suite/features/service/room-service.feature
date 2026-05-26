# Scope: service (single request/reply, no cross-service pipeline)
# Service: room-service — NATS request/reply handlers
#
# This file covers handlers NOT already exercised by room.feature
# (channel create) and room-role-update.feature (role-update validations).
#
# Handlers covered:
#   - handleCreateRoom (DM / self-DM / channel-name validation variants)
#   - handleAddMembers
#   - handleRemoveMember
#   - handleListMembers
#   - handleRoomsInfoBatch
#   - handleMessageReadReceipt
#
# Sources consulted:
#   - room-service/handler.go
#   - room-service/helper.go
#   - docs/superpowers/specs/2026-04-14-add-member-design.md
#   - docs/superpowers/specs/2026-04-14-remove-member-design.md
#   - docs/superpowers/specs/2026-04-14-room-info-batch-rpc-design.md
#   - docs/superpowers/specs/2026-04-20-room-members-list-design.md
#   - docs/superpowers/specs/2026-04-22-remove-member-hardening-design.md
#   - docs/superpowers/specs/2026-05-08-room-service-read-receipt-rpc-design.md

Feature: Room service — member operations and supporting RPCs

  # ---------------------------------------------------------------------------
  # CREATE ROOM — validation variants (DM / edge cases)
  # Avoids duplicating the channel-happy-path in room.feature
  # ---------------------------------------------------------------------------

  # Source: room-service/helper.go::classifyAndValidate
  #   errSelfDM = errors.New("cannot create a DM with yourself")
  # Category: state-preconditions
  @smoke
  Scenario: Creating a DM with yourself is rejected
    # NEW STEP: ^"([^"]+)" requests to create a DM with user "([^"]+)"$
    Given user "alice" is authenticated
    When "alice" requests to create a DM with user "alice"
    Then the response is a Validation error

  # Source: room-service/handler.go::handleCreateRoom — errMissingRequestID
  #   "missing X-Request-ID header" is returned before any DB call when the
  #   request context carries no request ID from headers.
  # Category: missing-request-id
  # Note: the integration harness (natsRequest_test.go) sets traceparent but
  # NOT X-Request-ID. wrappedCtx/ContextWithRequestIDFromHeaders returns "" →
  # handleCreateRoom returns errMissingRequestID immediately.
  # This behavior is observable without infrastructure (no room seeding needed).
  @blindspot:room-create-missing-request-id
  Scenario: Create room with no X-Request-ID header is rejected
    Given user "alice" is authenticated
    When "alice" requests to create a channel room without a request ID header
    Then the response is a Validation error

  # Source: room-service/helper.go::classifyAndValidate
  #   errChannelNameTooLong = errors.New("channel name must be at most 100 characters")
  #   const maxChannelNameRunes = 100
  # Category: field-boundary-conditions
  @smoke
  Scenario: Creating a channel with a name longer than 100 runes is rejected
    # NEW STEP: ^"([^"]+)" requests to create a channel room with a name of (\d+) runes$
    Given user "alice" is authenticated
    When "alice" requests to create a channel room with a name of 101 runes
    Then the response is a Validation error

  # Source: room-service/helper.go::classifyAndValidate
  #   errEmptyCreateRequest = errors.New("request must include at least one of users, orgs, channels, or name")
  # Category: missing-required-fields
  @smoke
  Scenario: Creating a room with no users, orgs, channels, or name is rejected
    # NEW STEP: ^"([^"]+)" requests to create an empty room$
    Given user "alice" is authenticated
    When "alice" requests to create an empty room
    Then the response is a Validation error

  # Source: room-service/helper.go::classifyAndValidate
  #   errBotInChannel = errors.New("bots cannot be added to a channel")
  #   isBot() matches ".bot" suffix or "p_" prefix accounts
  # Category: state-preconditions
  @smoke
  Scenario: Adding a bot account to a channel create request is rejected
    # NEW STEP: ^"([^"]+)" requests to create a channel room "([^"]+)" with bot user "([^"]+)"$
    Given user "alice" is authenticated
    When "alice" requests to create a channel room "bots-test" with bot user "myapp.bot"
    Then the response is a Validation error

  # Source: room-service/handler.go::handleCreateRoom
  #   natsCreateRoom special-cases dmExistsError: replies {"error":"dm already exists","roomId":"<id>"}
  # Category: idempotency / DM-already-exists
  @blindspot:room-create-dm-already-exists
  Scenario: Creating a DM that already exists returns the existing room ID
    # NEW STEP: ^"([^"]+)" requests to create a DM with user "([^"]+)"$
    Given user "alice" is authenticated
    And user "bob" is authenticated
    When "alice" requests to create a DM with user "bob"
    And "alice" requests to create a DM with user "bob"
    Then the response contains a "dm already exists" error with an existing room ID

  # ---------------------------------------------------------------------------
  # ADD MEMBERS
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  #   § "Reply contract": reply {"status":"accepted"} on success
  # Category: happy-path
  @smoke
  Scenario: Room member adds another user to an unrestricted channel
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" has created channel room "add-member-happy"
    # NEW STEP: ^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)"$
    When "alice" requests to add member "bob" to room "add-member-happy"
    Then the response is successful

  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  #   § "Validation & Resolution" step 3 Restricted room guard:
  #   "only owners can add members to a restricted room"
  # Category: authorization
  @smoke
  Scenario: A non-owner member cannot add to a restricted channel
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And user "carol" is authenticated
    And "alice" has created channel room "restricted-add-test"
    And "alice" has added "bob" to room "restricted-add-test"
    And room "restricted-add-test" is marked as restricted
    # NEW STEP: ^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)"$
    When "bob" requests to add member "carol" to room "restricted-add-test"
    Then the response is a Validation error

  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  #   § "Validation & Resolution" step 3: room type guard
  #   "cannot add members to a non-channel room"
  # Category: state-preconditions
  @blindspot:room-add-member-to-dm
  Scenario: Adding a member to a DM room is rejected
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" has created a DM room with "bob"
    # NEW STEP: ^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)"$
    # NEW STEP: ^"([^"]+)" has created a DM room with "([^"]+)"$
    When "alice" requests to add member "alice" to the DM room with "bob"
    Then the response is a Validation error

  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  #   § classifyAndValidate / errBotInChannel:
  #   "Reject direct bots up front — a client that explicitly lists a bot
  #    must see a hard error rather than a silent drop."
  # Category: state-preconditions
  @smoke
  Scenario: Explicitly adding a bot account to a channel via add-member is rejected
    Given user "alice" is authenticated
    And "alice" has created channel room "no-bots-channel"
    When "alice" requests to add member "myapp.bot" to room "no-bots-channel"
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # REMOVE MEMBER
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-14-remove-member-design.md
  #   § "Validation Rules" step 6: reply {"status":"accepted"} on success
  # Category: happy-path (self-leave)
  @smoke
  Scenario: A member can remove themselves from a channel (self-leave)
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" has created channel room "self-leave-room"
    And "alice" has added "bob" to room "self-leave-room"
    # NEW STEP: ^"([^"]+)" requests to remove member "([^"]+)" from room "([^"]+)"$
    When "bob" requests to remove member "bob" from room "self-leave-room"
    Then the response is successful

  # Source: docs/superpowers/specs/2026-04-14-remove-member-design.md
  #   § "Validation Rules" step 4a: org-only guard
  #   "org members cannot leave individually"
  # Category: state-preconditions
  @blindspot:room-remove-member-org-only
  Scenario: An org-only member cannot leave individually
    # This scenario requires org membership seeding (Part 2 Mongo primitive).
    # Marking as blindspot: the end-to-end observable behavior depends on
    # org-member state that cannot be set up via the current suite primitives.
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" has created channel room "org-remove-test"
    When "bob" requests to remove member "bob" from room "org-remove-test"
    Then the response is a Validation error

  # Source: docs/superpowers/specs/2026-04-14-remove-member-design.md
  #   § "Validation Rules" step 4c: last-owner guard
  #   "last owner cannot leave the room"
  # Source: room-service/helper.go — fmt.Errorf("last owner cannot leave the room")
  # Category: state-preconditions
  @smoke
  Scenario: The last owner cannot remove themselves from a channel
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" has created channel room "last-owner-room"
    And "alice" has added "bob" to room "last-owner-room"
    When "alice" requests to remove member "alice" from room "last-owner-room"
    Then the response is a Validation error

  # Source: docs/superpowers/specs/2026-04-14-remove-member-design.md
  #   § "Validation Rules" step 4b: owner authorization
  #   "only owners can remove members"
  # Category: authorization
  @smoke
  Scenario: A non-owner cannot remove another member from a channel
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And user "carol" is authenticated
    And "alice" has created channel room "remove-auth-room"
    And "alice" has added "bob" to room "remove-auth-room"
    And "alice" has added "carol" to room "remove-auth-room"
    When "bob" requests to remove member "carol" from room "remove-auth-room"
    Then the response is a Validation error

  # Source: docs/superpowers/specs/2026-04-14-remove-member-design.md
  #   § "Validation Rules" step 3: exactly one of account or orgId
  #   "exactly one of account or orgId must be set"
  # Category: missing-required-fields
  @smoke
  Scenario: Remove member with neither account nor orgId is rejected
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" has created channel room "remove-noargs-room"
    And "alice" has added "bob" to room "remove-noargs-room"
    # NEW STEP: ^"([^"]+)" requests to remove a member from room "([^"]+)" with no account or orgId$
    When "alice" requests to remove a member from room "remove-noargs-room" with no account or orgId
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # LIST MEMBERS
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-20-room-members-list-design.md
  #   § "Handler Layer" business logic:
  #   "GetSubscription → ErrSubscriptionNotFound → errNotRoomMember"
  # Category: authorization
  @smoke
  Scenario: A non-member cannot list the members of a room
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" has created channel room "members-auth-room"
    # NEW STEP: ^"([^"]+)" requests to list members of room "([^"]+)"$
    When "bob" requests to list members of room "members-auth-room"
    Then the response is a Validation error

  # Source: docs/superpowers/specs/2026-04-20-room-members-list-design.md
  #   § "Handler Layer" business logic (happy path)
  #   Subscribed requester receives ListRoomMembersResponse{Members:[...]}
  # Category: happy-path
  @smoke
  Scenario: A subscribed member can list the members of their room
    Given user "alice" is authenticated
    And "alice" has created channel room "list-members-happy"
    When "alice" requests to list members of room "list-members-happy"
    Then the response is successful

  # Source: docs/superpowers/specs/2026-04-20-room-members-list-design.md
  #   § handler: "limit must be > 0"
  # Category: field-boundary-conditions
  @smoke
  Scenario: Listing members with a zero limit is rejected
    Given user "alice" is authenticated
    And "alice" has created channel room "list-members-limit-room"
    # NEW STEP: ^"([^"]+)" requests to list members of room "([^"]+)" with limit (\d+)$
    When "alice" requests to list members of room "list-members-limit-room" with limit 0
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # ROOMS INFO BATCH
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-14-room-info-batch-rpc-design.md
  #   § 2.3 Validation: "len(RoomIDs) == 0 → roomIds must not be empty"
  # Category: missing-required-fields
  @smoke
  Scenario: RoomsInfoBatch with an empty roomIds list is rejected
    # NEW STEP: ^the room info batch RPC is called with (\d+) room IDs$
    When the room info batch RPC is called with 0 room IDs
    Then the response is a Validation error

  # Source: docs/superpowers/specs/2026-04-14-room-info-batch-rpc-design.md
  #   § 2.3 Validation: "len(RoomIDs) > MAX_BATCH_SIZE → batch size N exceeds limit M"
  # Category: field-boundary-conditions
  @smoke
  Scenario: RoomsInfoBatch with too many room IDs is rejected
    When the room info batch RPC is called with 1001 room IDs
    Then the response is a Validation error

  # Source: docs/superpowers/specs/2026-04-14-room-info-batch-rpc-design.md
  #   § 2.3 Response semantics: Found=false entry for a missing room ID
  # Category: existence
  @smoke
  Scenario: RoomsInfoBatch for an unknown room returns Found=false for that entry
    # NEW STEP: ^the room info batch RPC is called with room ID "([^"]+)"$
    # NEW STEP: ^the response contains a room info entry with "([^"]+)" set to "([^"]+)"$
    When the room info batch RPC is called with room ID "nonexistent-room-id"
    Then the response is successful
    And the response contains a room info entry with "found" set to "false"

  # ---------------------------------------------------------------------------
  # READ RECEIPT
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-05-08-room-service-read-receipt-rpc-design.md
  #   § Handler Flow step 3: ErrSubscriptionNotFound → errNotRoomMember
  # Category: authorization
  @smoke
  Scenario: A non-member cannot query read receipts for a message
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" has created channel room "receipt-auth-room"
    # NEW STEP: ^"([^"]+)" requests read receipts for message "([^"]+)" in room "([^"]+)"$
    When "bob" requests read receipts for message "some-msg-id" in room "receipt-auth-room"
    Then the response is a Validation error

  # Source: docs/superpowers/specs/2026-05-08-room-service-read-receipt-rpc-design.md
  #   § Handler Flow step 2: "reject empty MessageID"
  # Category: missing-required-fields
  @smoke
  Scenario: Read receipt request with an empty message ID is rejected
    Given user "alice" is authenticated
    And "alice" has created channel room "receipt-empty-msgid-room"
    # NEW STEP: ^"([^"]+)" requests read receipts with no message ID in room "([^"]+)"$
    When "alice" requests read receipts with no message ID in room "receipt-empty-msgid-room"
    Then the response is a Validation error

  # Source: docs/superpowers/specs/2026-05-08-room-service-read-receipt-rpc-design.md
  #   § Handler Flow step 6: "msgSender != requesterAccount → errNotMessageSender"
  #   "only the message sender can view read receipts"
  # Category: authorization
  @blindspot:room-read-receipt-non-sender
  Scenario: A member who is not the message sender cannot view read receipts
    # The suite lacks a primitive to create a real message and confirm its
    # message ID end-to-end (requires JetStream + message-worker pipeline,
    # Part 2). Marking as blindspot: the non-sender guard path requires
    # a seeded message with a known sender.
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" has created channel room "receipt-sender-room"
    And "alice" has added "bob" to room "receipt-sender-room"
    When "bob" requests read receipts for message "alice-message-id" in room "receipt-sender-room"
    Then the response is a Validation error

# ---------------------------------------------------------------------------
# TODO: additional scenarios for follow-up
# ---------------------------------------------------------------------------
#
# TODO: add-member capacity exceeded (MAX_ROOM_SIZE) — needs a room pre-seeded
#       with MAX_ROOM_SIZE-1 members, then one more add; requires Part 2
#       Mongo seeding primitive.
#       Source: room-service/handler.go::handleAddMembers — "room is at maximum capacity"
#
# TODO: create channel via channel-ref expansion — requester not a member of
#       source channel → errNotRoomMember.
#       Source: room-service/handler.go::expandChannelRefs
#
# TODO: list members with enrich=true returns display fields (engName, isOwner).
#       Source: docs/superpowers/specs/2026-04-20-room-members-list-design.md § wire example
#       Requires Part 2 Mongo observation to verify enriched fields.
#
# TODO: remove-member exactly one of account/orgId both set is rejected.
#       Source: room-service/handler.go::handleRemoveMember — "exactly one of account or orgId must be set"
#       Add a step that sends both fields.
#
# TODO: role-update org-only target cannot be promoted to owner.
#       Source: docs/superpowers/specs/2026-04-22-remove-member-hardening-design.md §
#       "errPromoteRequiresIndividual". Requires Part 2 Mongo seeding to
#       establish org-only membership before the promote request.
#
# TODO: rooms info batch — happy path with a known room returns Found=true and
#       correct name. Requires either a pre-seeded room or a create-then-batch
#       flow (multi-step, Part 2 or journey/ scope).
