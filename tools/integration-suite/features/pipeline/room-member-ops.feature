Feature: Room member operations pipeline — room-worker async processing
  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  # Source: docs/superpowers/specs/2026-04-14-remove-member-design.md
  # Source: docs/superpowers/specs/2026-04-13-role-update-design.md
  # Source: docs/superpowers/specs/2026-04-22-remove-member-hardening-design.md
  # Source: docs/superpowers/specs/2026-05-13-room-worker-membership-fixes-design.md
  #
  # Architecture notes
  # ------------------
  # room-worker is an async JetStream consumer. The client interacts with
  # room-service over NATS request/reply; room-service validates and replies
  # synchronously with {"status":"accepted"} (or an error). The canonical
  # event is then published to the ROOMS_{siteID} JetStream stream; room-worker
  # consumes it and publishes an AsyncJobResult to
  # chat.user.{account}.response.{requestID}.
  #
  # Part-1 testability boundary
  # ---------------------------
  # The synchronous room-service acceptance/rejection reply is observable in
  # Part-1 via NATS request/reply. The room-worker's downstream actions
  # (Mongo writes, outbox events, async AsyncJobResult on the response subject)
  # require either:
  #   (a) a pre-armed NATS subscription on the response subject before the
  #       JetStream message is published — the Part-2 "JetStream + decoupled
  #       reply" primitive, or
  #   (b) Mongo / JetStream observation steps — also Part-2.
  # These downstream contracts are tagged @blindspot until those primitives
  # exist. See docs/integration-suite/blindspots-room-worker.md for the full
  # blindspot register fragment.

  # ─── ADD MEMBERS ────────────────────────────────────────────────────────────

  # Category: Happy-path reply (room-service sync acceptance)
  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  #   § "Validation & Resolution (room-service)" step 9: "Reply {"status":"accepted"}"
  Scenario: Owner adds a new member to a channel and room-service accepts the request
    # NEW STEP: ^"([^"]+)" is the owner of channel room "([^"]+)"$
    # NEW STEP: ^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)"$
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" is the owner of channel room "project-alpha"
    When "alice" requests to add member "bob" to room "project-alpha"
    Then the response is successful

  # Category: Validation rejection — missing X-Request-ID
  # Source: room-worker/handler.go::processAddMembers
  #   "if requestID == "" { return newPermanent("missing X-Request-ID") }"
  # Note: room-service forwards the X-Request-ID header; the test exercises
  # what happens when no request ID is present in the NATS headers. In
  # practice the suite always sends a request ID via the natsRequest helper
  # (it sets X-Request-ID in the NATS header); this scenario is therefore a
  # blindspot until the suite can suppress the header.
  @blindspot:room-worker-add-missing-request-id
  Scenario: Add-member event missing X-Request-ID is rejected permanently by room-worker
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" is the owner of channel room "project-beta"
    When "alice" requests to add member "bob" to room "project-beta" without a request ID
    Then within 5s "alice" receives an async job error for operation "room.member.add"

  # Category: Validation rejection — add to non-channel room
  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  #   § "Validation & Resolution (room-service)":
  #   "Room type guard: If room.Type != RoomTypeChannel, reject with error
  #    "cannot add members to a DM room""
  # Note: room-service enforces this before publishing; the error arrives in
  # the synchronous reply from room-service, not from room-worker.
  Scenario: Adding members to a DM room is rejected by room-service
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And a DM room exists between "alice" and "bob"
    # NEW STEP: ^"([^"]+)" requests to add member "([^"]+)" to DM room between "([^"]+)" and "([^"]+)"$
    When "alice" requests to add "alice" to the DM room between "alice" and "bob"
    Then the response is a HandlerError error

  # Category: Authorization rejection — restricted room, non-owner requester
  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  #   § "Validation & Resolution (room-service)":
  #   "Restricted room guard: If room.Restricted == true and requester does not
  #    have owner role, reject with error "only owners can add members to this room""
  Scenario: Non-owner cannot add members to a restricted channel
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And user "carol" is authenticated
    And "alice" is the owner of restricted channel room "classified"
    And "bob" is a member of room "classified"
    When "bob" requests to add member "carol" to room "classified"
    Then the response is a HandlerError error

  # Category: State precondition — room does not exist
  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  #   § "Validation & Resolution (room-service)" step 2:
  #   "GetSubscription(requester, roomID) confirms requester is in the room"
  # A non-existent room means no subscription, so this fails the subscription
  # check before even reaching the room-type guard.
  Scenario: Adding a member to a non-existent room is rejected
    Given user "alice" is authenticated
    And user "bob" is authenticated
    # NEW STEP: ^"([^"]+)" requests to add member "([^"]+)" to room "([^"]+)" that does not exist$
    When "alice" requests to add member "bob" to room "nonexistent-room-abc" that does not exist
    Then the response is a HandlerError error

  # Category: Persistence post-condition (blindspot until Part-2 Mongo observation)
  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  #   § "Add Members (room-worker)" step 2:
  #   "BulkCreateSubscriptions — For each user, create Subscription with
  #    SiteID = room.SiteID, Roles = [RoleMember], JoinedAt = now"
  @blindspot:room-worker-add-member-persistence
  Scenario: After add-member succeeds a subscription document is written for the new member
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" is the owner of channel room "project-gamma"
    When "alice" requests to add member "bob" to room "project-gamma"
    Then the response is successful
    And within 5s a subscription exists in MongoDB for "bob" in room "project-gamma"

  # Category: Outbox propagation (blindspot until Part-2 JetStream observation)
  # Source: docs/superpowers/specs/2026-04-14-add-member-design.md
  #   § "Add Members (room-worker)" step 8:
  #   "Outbox for cross-site members (batched by destination site) — publish to
  #    outbox.{room.SiteID}.to.{destSiteID}.member_added"
  @blindspot:room-worker-add-member-outbox
  Scenario: Adding a cross-site member produces an outbox event on the room-worker pipeline
    Given user "alice" is authenticated
    And user "dave" is authenticated in site "site-remote"
    And "alice" is the owner of channel room "cross-site-room"
    When "alice" requests to add member "dave" to room "cross-site-room"
    Then the response is successful
    And within 5s an outbox event of type "member_added" is published to site "site-remote"

  # ─── REMOVE MEMBER (INDIVIDUAL) ─────────────────────────────────────────────

  # Category: Happy-path reply — self-leave acceptance
  # Source: docs/superpowers/specs/2026-04-14-remove-member-design.md
  #   § "Validation Rules (room-service)" step 6:
  #   "On success, publish the validated request to subject.RoomCanonical
  #    and reply {"status":"accepted"}"
  Scenario: A member self-leaves a channel and room-service accepts the request
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" is the owner of channel room "project-delta"
    And "bob" is a member of room "project-delta"
    # NEW STEP: ^"([^"]+)" requests to leave room "([^"]+)"$
    When "bob" requests to leave room "project-delta"
    Then the response is successful

  # Category: Authorization rejection — non-owner cannot remove another member
  # Source: docs/superpowers/specs/2026-04-14-remove-member-design.md
  #   § "Validation Rules (room-service)" step 4b:
  #   "Owner authorization: reject if the requester's Roles do not contain 'owner'"
  Scenario: Non-owner cannot remove another member from a channel
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And user "carol" is authenticated
    And "alice" is the owner of channel room "project-epsilon"
    And "bob" is a member of room "project-epsilon"
    And "carol" is a member of room "project-epsilon"
    # NEW STEP: ^"([^"]+)" requests to remove member "([^"]+)" from room "([^"]+)"$
    When "bob" requests to remove member "carol" from room "project-epsilon"
    Then the response is a HandlerError error

  # Category: State precondition — last-owner protection
  # Source: docs/superpowers/specs/2026-04-14-remove-member-design.md
  #   § "Validation Rules (room-service)" step 4c:
  #   "Last-owner guard: reject if target has the 'owner' role AND ownerCount <= 1"
  Scenario: The last owner cannot be removed from a channel
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" is the owner of channel room "project-zeta"
    And "bob" is a member of room "project-zeta"
    When "bob" requests to remove member "alice" from room "project-zeta"
    Then the response is a HandlerError error

  # Category: Persistence post-condition (blindspot until Part-2 Mongo observation)
  # Source: docs/superpowers/specs/2026-04-14-remove-member-design.md
  #   § "Processing Order (room-worker)" individual removal step 3a:
  #   "Delete subscription by (roomId, account)"
  @blindspot:room-worker-remove-member-persistence
  Scenario: After a successful self-leave the subscription document is deleted
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" is the owner of channel room "project-eta"
    And "bob" is a member of room "project-eta"
    When "bob" requests to leave room "project-eta"
    Then the response is successful
    And within 5s no subscription exists in MongoDB for "bob" in room "project-eta"

  # ─── ROLE UPDATE ────────────────────────────────────────────────────────────

  # Category: Happy-path reply — promote to owner
  # Source: docs/superpowers/specs/2026-04-13-role-update-design.md
  #   § "Validation Rules (room-service)" success path:
  #   "On success: marshal the request, publish to the ROOMS stream,
  #    reply {"status":"accepted"}"
  Scenario: Owner promotes a member to owner and room-service accepts the request
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" is the owner of channel room "project-theta"
    And "bob" is a member of room "project-theta"
    When "alice" requests role "owner" for member "bob" in room "project-theta"
    Then the response is successful

  # Category: Happy-path reply — demote from owner
  # Source: docs/superpowers/specs/2026-04-13-role-update-design.md
  #   § processRoleUpdate: "Demote: AddRole("member") then RemoveRole("owner")"
  Scenario: Owner demotes a co-owner to member and room-service accepts the request
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" is the owner of channel room "project-iota"
    And "bob" is an owner of room "project-iota"
    When "alice" requests role "member" for member "bob" in room "project-iota"
    Then the response is successful

  # Category: Authorization rejection — non-owner cannot update roles
  # Source: docs/superpowers/specs/2026-04-13-role-update-design.md
  #   § "Validation Rules (room-service)" rule #5:
  #   "Requester must have a subscription with HasRole(sub.Roles, RoleOwner):
  #    "only owners can update roles""
  Scenario: Non-owner cannot update another member's role
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And user "carol" is authenticated
    And "alice" is the owner of channel room "project-kappa"
    And "bob" is a member of room "project-kappa"
    And "carol" is a member of room "project-kappa"
    When "bob" requests role "owner" for member "carol" in room "project-kappa"
    Then the response is a HandlerError error

  # Category: State precondition — last-owner self-demote guard
  # Source: docs/superpowers/specs/2026-04-13-role-update-design.md
  #   § "Validation Rules (room-service)" rule #8:
  #   "If demoting and requester == target, CountOwners(roomID) > 1:
  #    "cannot demote: you are the last owner""
  Scenario: The last owner cannot self-demote
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" is the owner of channel room "project-lambda"
    And "bob" is a member of room "project-lambda"
    When "alice" requests role "member" for member "alice" in room "project-lambda"
    Then the response is a HandlerError error

  # Category: Persistence post-condition (blindspot until Part-2 Mongo observation)
  # Source: docs/superpowers/specs/2026-04-13-role-update-design.md
  #   § "processRoleUpdate" steps 2–4:
  #   "Promote: AddRole("owner"). Re-read subscription via GetSubscription.
  #    Publish SubscriptionUpdateEvent with action "role_updated"."
  @blindspot:room-worker-role-update-persistence
  Scenario: After a successful role promotion the subscription carries the owner role
    Given user "alice" is authenticated
    And user "bob" is authenticated
    And "alice" is the owner of channel room "project-mu"
    And "bob" is a member of room "project-mu"
    When "alice" requests role "owner" for member "bob" in room "project-mu"
    Then the response is successful
    And within 5s "bob"'s subscription in room "project-mu" contains role "owner"

  # Category: Outbox propagation (blindspot until Part-2 JetStream observation)
  # Source: docs/superpowers/specs/2026-04-13-role-update-design.md
  #   § "processRoleUpdate" step 6:
  #   "If user.SiteID != h.siteID: publish OutboxEvent with type "role_updated"
  #    to outbox.{room.SiteID}.to.{user.SiteID}.role_updated"
  @blindspot:room-worker-role-update-outbox
  Scenario: A role update for a cross-site member produces an outbox event
    Given user "alice" is authenticated
    And user "dave" is authenticated in site "site-remote"
    And "alice" is the owner of channel room "project-nu"
    And "dave" is a member of room "project-nu"
    When "alice" requests role "owner" for member "dave" in room "project-nu"
    Then the response is successful
    And within 5s an outbox event of type "role_updated" is published to site "site-remote"
