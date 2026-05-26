Feature: Messaging pipeline — message-gatekeeper field-level validation
  # Source: docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md
  #   § "Error Handling", § "Processing Flow"
  # Source: message-gatekeeper/handler.go
  #   — processMessage validation sequence (lines 122-231)
  # Source: docs/superpowers/specs/2026-04-27-message-quoting-design.md
  #   § "Architecture", § "Hard-fail policy"
  #
  # Transport note: message-gatekeeper is reached via a fire-and-forget
  # JetStream publish on MESSAGES_<site>; the gatekeeper's reply lands on
  # the decoupled subject chat.user.<account>.response.<requestId>.
  # Part-1 has only NATS request/reply primitives — no JetStream publish
  # or subscribe-and-wait primitive (deferred to Part 2).
  # ALL scenarios here are therefore blindspots until Part-2 primitives
  # land. They are tagged with the relevant blindspot slug so they fail
  # loudly and correctly rather than silently.
  #
  # Category abbreviations used in comments below:
  #   CAT-1  happy-path submit
  #   CAT-4  oversized body
  #   CAT-5  bad thread parent fields
  #   CAT-7  bad mention / quote list
  #   CAT-8  bad room subject (user not subscribed)
  #   CAT-9  edit-message validation
  #   CAT-10 delete-message validation

  # ---------------------------------------------------------------------------
  # CAT-1: Happy-path submit
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md
  #   § "Processing Flow" step 8: "Reply success: natsutil.ReplyJSON(msg, message)
  #     to chat.user.{username}.response.{requestID}"
  # Source: message-gatekeeper/handler.go:218
  #   evt := model.MessageEvent{Event: model.EventCreated, Message: msg, ...}
  # Category: CAT-1 — happy-path submit
  @blindspot:gatekeeper-canonical-publish-needs-jetstream-primitive @covers:messaging-submit-valid
  Scenario: Valid message submission is accepted and canonical event is published
    # NEW STEP: ^"([^"]+)" submits a valid message to room "([^"]+)"$
    # NEW STEP: ^the response is an accepted reply$
    # NEW STEP: ^within ([0-9]+)s a canonical created event exists for the message in room "([^"]+)"$
    Given user "alice" is authenticated
    When "alice" submits a valid message to room "general"
    Then the response is an accepted reply
    And within 5s a canonical created event exists for the message in room "general"

  # ---------------------------------------------------------------------------
  # CAT-4: Oversized body
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md
  #   § "Error Handling": "Content > 20KB → Reply error, ack message"
  # Source: message-gatekeeper/handler.go:21
  #   const maxContentBytes = 20 * 1024 // 20 KB
  # Source: message-gatekeeper/handler.go:149-151
  #   if len(req.Content) > maxContentBytes {
  #       return nil, fmt.Errorf("content exceeds maximum size of %d bytes", maxContentBytes)
  #   }
  # Category: CAT-4 — oversized body
  @blindspot:message-submit-needs-jetstream-primitive @covers:messaging-submit-oversized-content
  Scenario: Message with content exceeding 20 KB is rejected
    # NEW STEP: ^"([^"]+)" submits a message with content larger than 20 KB to room "([^"]+)"$
    Given user "alice" is authenticated
    When "alice" submits a message with content larger than 20 KB to room "general"
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # CAT-5: Bad thread parent fields — missing createdAt companion
  # ---------------------------------------------------------------------------

  # Source: message-gatekeeper/handler.go:154-156
  #   if req.ThreadParentMessageID != "" && req.ThreadParentMessageCreatedAt == nil {
  #       return nil, fmt.Errorf("validate thread parent fields: threadParentMessageCreatedAt
  #           is required when threadParentMessageId is set")
  #   }
  # Category: CAT-5 — bad thread parent fields
  @blindspot:message-submit-needs-jetstream-primitive @covers:messaging-submit-thread-parent-missing-created-at
  Scenario: Thread reply with threadParentMessageId but no threadParentMessageCreatedAt is rejected
    # NEW STEP: ^"([^"]+)" submits a thread reply to room "([^"]+)" with threadParentMessageId set but no threadParentMessageCreatedAt$
    Given user "alice" is authenticated
    When "alice" submits a thread reply to room "general" with threadParentMessageId set but no threadParentMessageCreatedAt
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # CAT-5: Bad thread parent fields — invalid thread parent message ID format
  # ---------------------------------------------------------------------------

  # Source: message-gatekeeper/handler.go:139-141
  #   if req.ThreadParentMessageID != "" && !idgen.IsValidMessageID(req.ThreadParentMessageID) {
  #       return nil, fmt.Errorf("invalid thread parent message ID %q: must be a 20-char base62 string",
  #           req.ThreadParentMessageID)
  #   }
  # Category: CAT-5 — bad thread parent fields
  @blindspot:message-submit-needs-jetstream-primitive @covers:messaging-submit-thread-parent-invalid-id
  Scenario: Thread reply with a malformed threadParentMessageId is rejected
    # NEW STEP: ^"([^"]+)" submits a thread reply to room "([^"]+)" with a malformed threadParentMessageId$
    Given user "alice" is authenticated
    When "alice" submits a thread reply to room "general" with a malformed threadParentMessageId
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # CAT-5: Bad thread parent fields — cross-thread quote context mismatch
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-27-message-quoting-design.md
  #   § "Hard-fail policy":
  #     "Parent has a different thread context (parent.ThreadParentID !=
  #      req.ThreadParentMessageID) → gatekeeper post-RPC check → request
  #      fails, error replied to client."
  # Source: message-gatekeeper/handler.go:249-253
  #   case snap.ThreadParentID != newMessageThreadID:
  #       return nil, fmt.Errorf("quoted parent %s thread context mismatch: ...")
  # Category: CAT-5 — bad thread parent fields (cross-thread quote)
  @blindspot:gatekeeper-quote-thread-context-check-needs-jetstream-primitive @covers:messaging-submit-quote-thread-mismatch
  Scenario: Quoting a message from a different thread context is rejected
    # NEW STEP: ^"([^"]+)" submits a message to room "([^"]+)" quoting a message from a different thread$
    Given user "alice" is authenticated
    When "alice" submits a message to room "general" quoting a message from a different thread
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # CAT-7: Bad quote — main-room message quoting a thread reply
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-27-message-quoting-design.md
  #   § "Scope": "Main-room messages can only quote main-room messages;
  #     messages inside thread T can only quote other messages in thread T."
  # Source: message-gatekeeper/handler.go:237-254 (resolveQuoteSnapshot)
  #   The snap.ThreadParentID check enforces the same-context rule.
  # Category: CAT-7 — bad quote / mention list
  @blindspot:gatekeeper-quote-thread-context-check-needs-jetstream-primitive @covers:messaging-submit-main-room-quoting-thread-reply
  Scenario: Main-room message quoting a thread reply is rejected
    # NEW STEP: ^"([^"]+)" submits a main-room message to room "([^"]+)" quoting a thread reply$
    Given user "alice" is authenticated
    When "alice" submits a main-room message to room "general" quoting a thread reply
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # CAT-7: Bad quote — referenced parent message not found
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-27-message-quoting-design.md
  #   § "Hard-fail policy":
  #     "Client sends invalid UUID for quotedParentMessageId → RPC returns
  #      NotFound → request fails, error replied to client"
  #     "Parent message not found in Cassandra → RPC returns NotFound →
  #      request fails, error replied to client"
  # Category: CAT-7 — bad quote list (non-existent parent)
  @blindspot:message-submit-needs-jetstream-primitive @covers:messaging-submit-quote-parent-not-found
  Scenario: Quoting a non-existent parent message fails the send
    # NEW STEP: ^"([^"]+)" submits a message to room "([^"]+)" quoting a non-existent message id$
    Given user "alice" is authenticated
    When "alice" submits a message to room "general" quoting a non-existent message id
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # CAT-8: Bad room subject — user not subscribed
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md
  #   § "Processing Flow" step 5:
  #     "Validate subscription: store.GetSubscription(ctx, username, roomID)
  #      — confirms membership, retrieves UserID"
  #   § "Error Handling":
  #     "User not in room → Reply error, ack message"
  # Source: message-gatekeeper/handler.go:159-165
  #   if errors.Is(err, errNotSubscribed) {
  #       return nil, fmt.Errorf("user %s is not subscribed to room %s", account, roomID)
  #   }
  # Category: CAT-8 — bad room subject (not subscribed)
  @blindspot:message-submit-needs-jetstream-primitive @covers:messaging-submit-not-subscribed
  Scenario: Message submitted by a user not subscribed to the target room is rejected
    # NEW STEP: ^"([^"]+)" submits a message to room "([^"]+)" on behalf of a non-member$
    Given user "alice" is authenticated
    And user "bob" is authenticated
    When "bob" submits a message to room "alice-private" on behalf of a non-member
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # CAT-8 / Large-room restriction — non-privileged user blocked in large room
  # ---------------------------------------------------------------------------

  # Source: message-gatekeeper/handler.go:173-189 (large-room post restriction)
  #   "in rooms with more than the configured threshold of members, only
  #    owners, admins, and bots may send top-level messages. Thread replies
  #    are exempt regardless of room size."
  # Source: docs/superpowers/specs/2026-05-18-message-pipeline-mongo-caching-design.md
  #   § "Changes to message-gatekeeper" (canBypassLargeRoomCap described)
  # Category: CAT-8 — large-room post restriction (Part-2: requires Mongo seeding)
  @blindspot:gatekeeper-large-room-cap-requires-mongo-seeding @covers:messaging-submit-large-room-blocked
  Scenario: Non-privileged user is blocked from top-level posting in a large room
    # NEW STEP: ^a channel room "([^"]+)" exists with more than ([0-9]+) members$
    # NEW STEP: ^"([^"]+)" is a regular member of room "([^"]+)"$
    # NEW STEP: ^"([^"]+)" submits a top-level message to room "([^"]+)"$
    Given user "alice" is authenticated
    And a channel room "crowded" exists with more than 500 members
    And "alice" is a regular member of room "crowded"
    When "alice" submits a top-level message to room "crowded"
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # CAT-8 / Large-room restriction — thread reply exempt from large-room cap
  # ---------------------------------------------------------------------------

  # Source: message-gatekeeper/handler.go:173
  #   isThreadReply := req.ThreadParentMessageID != ""
  #   if !isThreadReply && !canBypassLargeRoomCap(sub) {
  # Category: CAT-8 — large-room restriction exemption for thread replies
  @blindspot:gatekeeper-large-room-cap-requires-mongo-seeding @covers:messaging-submit-large-room-thread-reply-exempt
  Scenario: Thread reply from a non-privileged user is accepted even in a large room
    # (reuses NEW STEPs declared in previous scenario)
    Given user "alice" is authenticated
    And a channel room "crowded" exists with more than 500 members
    And "alice" is a regular member of room "crowded"
    When "alice" submits a thread reply to room "crowded" with threadParentMessageId set but no threadParentMessageCreatedAt
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # CAT-9: Edit-message validation — subscription gate (history-service)
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-22-edit-message-design.md
  #   § "3. Authorization Model":
  #     "Non-subscribers receive ErrForbidden('not subscribed to room')
  #      before any message lookup."
  # Source: docs/superpowers/specs/2026-04-22-edit-message-design.md
  #   § "5. Data Flow & Error Handling": subscription check → ErrForbidden
  # NOTE: Edit lives in history-service, not message-gatekeeper; the
  # subject is chat.user.{account}.request.room.{roomID}.{siteID}.msg.edit.
  # This is a NATS request/reply and IS observable in Part 1 via natsRequest.
  # Category: CAT-9 — edit-message validation
  @covers:messaging-edit-non-subscriber-forbidden
  Scenario: Non-subscriber cannot edit a message in a room they do not belong to
    # NEW STEP: ^"([^"]+)" requests to edit message "([^"]+)" in room "([^"]+)" with new content "([^"]+)"$
    Given user "alice" is authenticated
    And user "bob" is authenticated
    When "bob" requests to edit message "someMessageId" in room "alices-room" with new content "updated"
    Then the response is a Auth error

  # ---------------------------------------------------------------------------
  # CAT-9: Edit-message validation — non-sender forbidden (history-service)
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-22-edit-message-design.md
  #   § "3. Authorization Model":
  #     "Anyone else (including room owners) receives
  #      ErrForbidden('only the sender can edit')."
  # Category: CAT-9 — edit-message sender gate
  @blindspot:gatekeeper-edit-sender-gate-requires-seeded-message @covers:messaging-edit-non-sender-forbidden
  Scenario: Subscriber cannot edit a message authored by another user
    Given user "alice" is authenticated
    And user "bob" is authenticated
    When "bob" requests to edit message "someMessageId" in room "alices-room" with new content "updated"
    Then the response is a Auth error

  # ---------------------------------------------------------------------------
  # CAT-9: Edit-message validation — empty newMsg rejected (history-service)
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-22-edit-message-design.md
  #   § "5. Data Flow & Error Handling":
  #     "Content validation: trimmed non-empty, raw ≤ 20 KB →
  #      ErrBadRequest('newMsg must not be empty')"
  # Category: CAT-9 — edit content validation
  @blindspot:gatekeeper-edit-empty-content-requires-seeded-message @covers:messaging-edit-empty-content-rejected
  Scenario: Edit request with empty newMsg is rejected
    # NEW STEP: ^"([^"]+)" requests to edit message "([^"]+)" in room "([^"]+)" with empty content$
    Given user "alice" is authenticated
    When "alice" requests to edit message "someMessageId" in room "general" with empty content
    Then the response is a Validation error

  # ---------------------------------------------------------------------------
  # CAT-10: Delete-message validation — non-subscriber forbidden (history-service)
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-22-delete-message-design.md
  #   § "3. Authorization Model":
  #     "Non-subscribers receive ErrForbidden('not subscribed to room')
  #      before any message lookup."
  # Category: CAT-10 — delete-message validation
  @covers:messaging-delete-non-subscriber-forbidden
  Scenario: Non-subscriber cannot delete a message in a room they do not belong to
    # NEW STEP: ^"([^"]+)" requests to delete message "([^"]+)" in room "([^"]+)"$
    Given user "alice" is authenticated
    And user "bob" is authenticated
    When "bob" requests to delete message "someMessageId" in room "alices-room"
    Then the response is a Auth error

  # ---------------------------------------------------------------------------
  # CAT-10: Delete-message validation — non-sender forbidden (history-service)
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-04-22-delete-message-design.md
  #   § "3. Authorization Model":
  #     "Anyone else (including room owners) receives
  #      ErrForbidden('only the sender can delete')."
  # Category: CAT-10 — delete-message sender gate
  @blindspot:gatekeeper-delete-sender-gate-requires-seeded-message @covers:messaging-delete-non-sender-forbidden
  Scenario: Subscriber cannot delete a message they did not author
    Given user "alice" is authenticated
    And user "bob" is authenticated
    When "bob" requests to delete message "someMessageId" in room "alices-room"
    Then the response is a Auth error

  # ---------------------------------------------------------------------------
  # Extra category: CAT-3 / siteID mismatch ack-and-drop
  # ---------------------------------------------------------------------------

  # Source: docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md
  #   § "Error Handling":
  #     "SiteID mismatch → Log error, ack (misconfigured)"
  # Source: message-gatekeeper/handler.go:124-126
  #   if siteID != h.siteID {
  #       return nil, fmt.Errorf("siteID mismatch: got %s, want %s", siteID, h.siteID)
  #   }
  # NOTE: a siteID-mismatched message is silently acked with no client reply
  # (no RequestID reply is sent for this code path). The "no reply" contract
  # is unverifiable without JetStream observation (Part 2).
  # Category: extra — siteID mismatch silent-drop
  @blindspot:gatekeeper-siteid-mismatch-reply-unobservable @covers:messaging-submit-siteid-mismatch-drop
  Scenario: Message published on a mismatched siteID subject is silently acked with no reply
    # NEW STEP: ^"([^"]+)" publishes a message on a subject for site "([^"]+)" to room "([^"]+)"$
    # NEW STEP: ^no reply is received on the response subject$
    Given user "alice" is authenticated
    When "alice" publishes a message on a subject for site "wrong-site" to room "general"
    Then no reply is received on the response subject
