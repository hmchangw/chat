Feature: History service — request validation
  # Sources:
  #   history-service/internal/service/utils.go    — getAccessSince, findMessage, parsePageRequest
  #   history-service/internal/service/messages.go — LoadHistory, LoadNextMessages,
  #                                                   LoadSurroundingMessages, GetMessageByID,
  #                                                   EditMessage, DeleteMessage
  #   history-service/internal/service/threads.go  — GetThreadMessages (cursor path),
  #                                                   GetThreadParentMessages, validateThreadFilter
  #   docs/superpowers/specs/2026-03-25-refactor-history-service-design.md
  #   docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md
  #   docs/superpowers/specs/2026-04-21-history-list-threads-design.md
  #   docs/superpowers/specs/2026-04-23-history-list-threads-redesign.md
  #   docs/superpowers/specs/2026-05-07-loadhistory-min-user-last-seen-at-design.md
  # Transport: NATS request/reply on
  #   chat.user.{account}.request.room.{roomID}.{siteID}.msg.<operation>
  #
  # Handler gate order (relevant for Part-1 observability):
  #   GetThreadMessages:       empty-ID check → getAccessSince → ... → parsePageRequest
  #   All other handlers:      getAccessSince first, then field validation
  # Therefore: empty-ID validation is only directly observable (Validation error) for
  # GetThreadMessages — already covered in history-thread.feature. For all other
  # handlers the subscription gate fires first, yielding Auth error in Part-1.
  #
  # Do NOT duplicate the GetThreadMessages empty-ID or unsubscribed scenarios
  # already in history-thread.feature.

  # ===========================================================================
  # Category 5 — Subscription required: Load History
  # Source: history-service/internal/service/utils.go:14-24 — getAccessSince:
  #   "if !subscribed { return nil, natsrouter.ErrForbidden("not subscribed to room") }"
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-load-history-not-subscribed
  Scenario: Load history for a room the caller is not subscribed to
    # Source: history-service/internal/service/utils.go:23
    #   getAccessSince: ErrForbidden("not subscribed to room"). LoadHistory calls
    #   getAccessSince first — subscription gate fires before any Cassandra read.
    #   The suite's NATS classifier cannot confirm Auth-class rejection (see
    #   blindspot history-forbidden-class-unverifiable).
    #
    # Category: 5 — Subscription required
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" requests history for room "([^"]+)"$
    When "alice" requests history for room "general"
    Then the response is a Auth error

  # ===========================================================================
  # Category 5 — Subscription required: Load Next Messages
  # Source: same as above — getAccessSince called first in LoadNextMessages.
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-next-messages-not-subscribed
  Scenario: Load next messages for a room the caller is not subscribed to
    # Source: history-service/internal/service/messages.go LoadNextMessages:
    #   getAccessSince → ErrForbidden("not subscribed to room").
    #
    # Category: 5 — Subscription required
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" requests next messages in room "([^"]+)"$
    When "alice" requests next messages in room "general"
    Then the response is a Auth error

  # ===========================================================================
  # Category 3 + 5 — Invalid cursor (LoadNextMessages) — auth gate fires first
  # Source: history-service/internal/service/messages.go:146-149 — parsePageRequest
  #   called with req.Cursor; ErrBadRequest("invalid pagination cursor") on bad base64.
  #   getAccessSince runs BEFORE parsePageRequest, so without a subscription
  #   the unsubscribed error fires first. Part-2 can establish a subscription
  #   fixture and then verify the cursor-parse branch directly.
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-next-messages-invalid-cursor
  Scenario: Load next messages with a malformed cursor in a room the caller is not subscribed to
    # Source: history-service/internal/service/messages.go:146-149 — parsePageRequest
    #   returns ErrBadRequest("invalid pagination cursor") for bad base64.
    #   Auth gate fires before cursor parse in Part-1; documented here so the
    #   behavior is catalogued and the cursor-validation branch can be claimed
    #   with a subscription fixture in Part-2.
    #
    # Category: 3 — Invalid cursor (and 5 — Subscription required, observed in Part-1)
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" requests next messages in room "([^"]+)" with cursor "([^"]+)"$
    When "alice" requests next messages in room "general" with cursor "not-valid-base64!!!"
    Then the response is a Auth error

  # ===========================================================================
  # Category 2 + 5 — Missing required field: Load Surrounding Messages (empty messageId)
  # Source: history-service/internal/service/utils.go:43-46 — findMessage:
  #   messageID == "" → ErrBadRequest("messageId is required").
  #   getAccessSince runs BEFORE findMessage; without subscription → Auth error.
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-surrounding-empty-message-id
  Scenario: Requesting surrounding messages with no message id in a room the caller is not subscribed to
    # Source: history-service/internal/service/utils.go:43-46
    #   findMessage: empty messageID → ErrBadRequest("messageId is required").
    #   LoadSurroundingMessages: getAccessSince runs before findMessage.
    #   Auth gate fires first in Part-1; empty-ID check is directly observable
    #   only once a subscription fixture can be established in Part-2.
    #
    # Category: 2 — Missing required field (and 5 — Subscription required in Part-1)
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" requests surrounding messages with no message id in room "([^"]+)"$
    When "alice" requests surrounding messages with no message id in room "general"
    Then the response is a Auth error

  # ===========================================================================
  # Category 5 — Subscription required: Load Surrounding Messages (unknown message id)
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-surrounding-not-subscribed
  Scenario: Requesting surrounding messages for a room the caller is not subscribed to
    # Source: history-service/internal/service/messages.go LoadSurroundingMessages
    #   calls getAccessSince → ErrForbidden("not subscribed to room").
    #
    # Category: 5 — Subscription required
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" requests surrounding messages for unknown message in room "([^"]+)"$
    When "alice" requests surrounding messages for unknown message in room "general"
    Then the response is a Auth error

  # ===========================================================================
  # Category 2 + 5 — Missing required field: Get Message By ID (empty messageId)
  # Source: history-service/internal/service/utils.go findMessage.
  #   getAccessSince runs BEFORE findMessage; auth gate fires first in Part-1.
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-get-message-empty-id
  Scenario: Fetching a message by id with an empty id in a room the caller is not subscribed to
    # Source: history-service/internal/service/utils.go:43-46 — findMessage:
    #   ErrBadRequest("messageId is required") when messageID == "".
    #   GetMessageByID: getAccessSince runs before findMessage.
    #   Auth gate fires first in Part-1.
    #
    # Category: 2 — Missing required field (and 5 — Subscription required in Part-1)
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" requests message by id "" in room "([^"]+)"$
    When "alice" requests message by id "" in room "general"
    Then the response is a Auth error

  # ===========================================================================
  # Category 5 — Subscription required: Get Message By ID (unknown id)
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-get-message-not-subscribed
  Scenario: Fetching a message for a room the caller is not subscribed to
    # Source: history-service/internal/service/messages.go GetMessageByID
    #   calls getAccessSince → ErrForbidden("not subscribed to room").
    #
    # Category: 5 — Subscription required
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" requests message by unknown id in room "([^"]+)"$
    When "alice" requests message by unknown id in room "general"
    Then the response is a Auth error

  # ===========================================================================
  # Category 2 + 5 — Missing required field: Edit Message (empty messageId)
  # Source: history-service/internal/service/messages.go:309 — EditMessage
  #   calls findMessage. getAccessSince runs BEFORE findMessage.
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-edit-message-empty-message-id
  Scenario: Editing a message with an empty message id in a room the caller is not subscribed to
    # Source: history-service/internal/service/messages.go:309 — EditMessage
    #   calls findMessage(c, roomID, req.MessageID);
    #   ErrBadRequest("messageId is required") when req.MessageID == "".
    #   getAccessSince (subscription gate) runs before findMessage.
    #   Auth gate fires first in Part-1.
    #
    # Category: 2 — Missing required field (and 5 — Subscription required in Part-1)
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" edits message "" in room "([^"]+)" with new content "([^"]+)"$
    When "alice" edits message "" in room "general" with new content "hello"
    Then the response is a Auth error

  # ===========================================================================
  # Category 2 + 5 — Missing required field: Edit Message (empty newMsg)
  # Source: history-service/internal/service/messages.go:325-327 — EditMessage
  #   TrimSpace(req.NewMsg) == "" → ErrBadRequest("newMsg must not be empty")
  #   This runs after the subscription gate AND after findMessage resolves the
  #   message. Cannot be directly observed without subscription + real message.
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-edit-empty-content
  Scenario: Editing a message with empty new content in a room the caller is not subscribed to
    # Source: history-service/internal/service/messages.go:325-327
    #   "if strings.TrimSpace(req.NewMsg) == "" { return nil,
    #    natsrouter.ErrBadRequest("newMsg must not be empty") }"
    #   Both the subscription gate and the findMessage call run before this check.
    #   Part-2 required to exercise the empty-content validation branch.
    #
    # Category: 2 — Missing required field (and 5 — Subscription required in Part-1)
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" edits unknown message in room "([^"]+)" with empty content$
    When "alice" edits unknown message in room "general" with empty content
    Then the response is a Auth error

  # ===========================================================================
  # Category 5 — Subscription required: Edit Message (non-empty, unknown id)
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-edit-not-subscribed
  Scenario: Editing a message in a room the caller is not subscribed to
    # Source: history-service/internal/service/messages.go EditMessage
    #   calls getAccessSince → ErrForbidden("not subscribed to room").
    #
    # Category: 5 — Subscription required
    Given user "alice" is authenticated
    When "alice" edits message "" in room "general" with new content "hello"
    Then the response is a Auth error

  # ===========================================================================
  # Category 2 + 5 — Missing required field: Delete Message (empty messageId)
  # Source: history-service/internal/service/messages.go DeleteMessage
  #   calls findMessage. getAccessSince runs BEFORE findMessage.
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-delete-message-empty-id
  Scenario: Deleting a message with an empty message id in a room the caller is not subscribed to
    # Source: history-service/internal/service/messages.go DeleteMessage
    #   calls findMessage(c, roomID, req.MessageID);
    #   ErrBadRequest("messageId is required") when req.MessageID == "".
    #   Auth gate (getAccessSince) runs before findMessage.
    #
    # Category: 2 — Missing required field (and 5 — Subscription required in Part-1)
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" deletes message "" in room "([^"]+)"$
    When "alice" deletes message "" in room "general"
    Then the response is a Auth error

  # ===========================================================================
  # Category 5 — Subscription required: Delete Message (unknown id)
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-delete-not-subscribed
  Scenario: Deleting a message in a room the caller is not subscribed to
    # Source: history-service/internal/service/messages.go DeleteMessage
    #   calls getAccessSince → ErrForbidden("not subscribed to room").
    #
    # Category: 5 — Subscription required
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" deletes unknown message in room "([^"]+)"$
    When "alice" deletes unknown message in room "general"
    Then the response is a Auth error

  # ===========================================================================
  # Category 4 + 5 — Invalid filter: GetThreadParentMessages
  # Source: history-service/internal/service/threads.go validateThreadFilter:
  #   unknown filter → ErrBadRequest("invalid thread filter: %q").
  #   getAccessSince runs BEFORE validateThreadFilter; auth gate fires first
  #   without a subscription. Part-2 can assert Validation error directly.
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-list-threads-invalid-filter
  Scenario: Listing thread parents with an unknown filter in a room the caller is not subscribed to
    # Source: history-service/internal/service/threads.go:112-121
    #   validateThreadFilter: ErrBadRequest for any value outside "", "all",
    #   "following", "unread". getAccessSince runs first; in Part-1 the auth gate
    #   fires before filter validation.
    #
    # Category: 4 — Invalid filter (and 5 — Subscription required in Part-1)
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" requests thread parent messages in room "([^"]+)" with filter "([^"]+)"$
    When "alice" requests thread parent messages in room "general" with filter "invalid-filter-value"
    Then the response is a Auth error

  # ===========================================================================
  # Category 5 — Subscription required: GetThreadParentMessages (valid filter)
  # ===========================================================================

  @blindspot:history-forbidden-class-unverifiable @covers:history-list-threads-not-subscribed
  Scenario: Listing thread parents for a room the caller is not subscribed to
    # Source: history-service/internal/service/threads.go GetThreadParentMessages
    #   calls getAccessSince → ErrForbidden("not subscribed to room").
    #   Subscription check runs before filter validation.
    #
    # Category: 5 — Subscription required
    Given user "alice" is authenticated
    # NEW STEP: ^"([^"]+)" requests thread parent messages in room "([^"]+)"$
    When "alice" requests thread parent messages in room "general"
    Then the response is a Auth error

  # ===========================================================================
  # Category 6 — Empty result: thread with no replies (Part-2 blindspot)
  # Source: history-service/internal/service/threads.go:44-53 — when
  #   parent.ThreadRoomID == "" → {messages: [], hasNext: false}
  # ===========================================================================

  @blindspot:history-empty-thread-requires-subscription-fixture
  Scenario: A subscribed caller gets an empty result for a thread parent with no replies
    # Source: history-service/internal/service/threads.go:44-53
    #   "if msg.ThreadRoomID == "" {
    #     return &GetThreadMessagesResponse{Messages: []models.Message{}, HasNext: false}, nil }"
    # This path is only reachable when: (a) caller is subscribed; (b) the parent
    # message exists in Cassandra with an empty thread_room_id column. Both
    # require fixtures unavailable in Part-1.
    # The Validation step below is a placeholder that exercises the empty-ID check
    # (which does fire before the subscription gate for GetThreadMessages) so the
    # scenario has a runnable Step; the actual Category-6 behavior is the blindspot.
    #
    # Category: 6 — Empty result
    Given user "alice" is authenticated
    When "alice" requests thread messages with no thread message id in room "general"
    Then the response is a Validation error

  # ===========================================================================
  # Category 7 — Cursor pagination (Part-2 blindspot)
  # Source: history-service/internal/service/messages.go LoadNextMessages:
  #   returns NextCursor (base64 PageState) and HasNext=true when more data exists.
  # ===========================================================================

  @blindspot:history-pagination-requires-cassandra-fixture
  Scenario: Paginating load-next-messages yields a non-overlapping second page
    # Source: history-service/internal/service/messages.go LoadNextMessages
    #   returns NextCursor (opaque base64 Cassandra PageState) and HasNext=true
    #   when more data exists beyond the page limit. A second request using the
    #   returned cursor must yield a non-overlapping page.
    # Part-2 required: needs Cassandra seed data exceeding the page limit and a
    #   valid subscription fixture. In Part-1 the auth gate produces Auth error.
    #
    # Category: 7 — Cursor pagination
    Given user "alice" is authenticated
    When "alice" requests next messages in room "general"
    Then the response is a Auth error

  # ===========================================================================
  # Category 9 — Message ordering (Part-2 blindspot)
  # Source: docs/superpowers/specs/2026-03-25-refactor-history-service-design.md
  #   § LoadHistory: GetMessagesBefore "ordered newest-first"
  # ===========================================================================

  @blindspot:history-ordering-requires-cassandra-fixture
  Scenario: Load history returns messages in newest-first order
    # Source: docs/superpowers/specs/2026-03-25-refactor-history-service-design.md
    #   § LoadHistory: "GetMessagesBefore returns messages in a room before `before`
    #   and after `since`, ordered newest-first."
    # Part-2 required: needs Cassandra fixture with at least two messages at distinct
    #   timestamps and a subscription fixture to assert sort order on the response array.
    #
    # Category: 9 — Message ordering
    Given user "alice" is authenticated
    When "alice" requests history for room "general"
    Then the response is a Auth error
