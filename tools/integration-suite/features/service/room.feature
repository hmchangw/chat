Feature: Room service — channel creation
  # Source: room-service/handler.go::handleCreateRoom (NATS subject chat.user.<account>.request.rooms.create)
  # The system exposes channel room creation as a NATS request/reply on the
  # requester's chat.user.<account>.request.rooms.create subject. The reply
  # is the created Room JSON on success, or {"error":"..."} on failure.

  @status:approved @smoke @covers:room-create-channel
  Scenario: Authenticated user creates a channel room
    Given user "alice" is authenticated
    When "alice" creates channel room "general"
    Then the response is successful

  Scenario: Authenticated user creating a channel room (draft demo)
    Given user "bob" is authenticated
    When "bob" creates channel room "draft-room"
    Then the response is successful
