Feature: History service — thread messages request validation
  # Source: docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md
  #   § "Error Matrix":
  #     | ThreadMessageID empty | ErrBadRequest | bad_request |
  #     | Caller not subscribed to parent's room | ErrForbidden("not subscribed to room") | forbidden |
  # Transport: NATS request/reply on
  #   chat.user.<account>.request.room.<roomID>.<site>.msg.thread
  # The empty-ID check runs before the room-access check, so it is
  # observable without a pre-existing room or subscription.

  @smoke @covers:history-thread-empty-id
  Scenario: Requesting a thread with no thread message id is rejected as invalid
    Given user "alice" is authenticated
    When "alice" requests thread messages with no thread message id in room "general"
    Then the response is a Validation error

  @blindspot:history-forbidden-class-unverifiable @covers:history-thread-not-subscribed
  Scenario: Requesting a thread in a room the caller is not subscribed to
    # Design says ErrForbidden("not subscribed to room"). The suite's
    # NATS classifier is substring-based; the sanitized message contains
    # no "forbidden"/"unauthorized"/"permission" token, so it cannot be
    # confirmed as an Auth-class rejection. See blindspots register.
    Given user "alice" is authenticated
    When "alice" requests thread messages for an unknown message in room "general"
    Then the response is a Auth error
