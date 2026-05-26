Feature: Room service — member role update validation
  # Source: docs/superpowers/specs/2026-04-13-role-update-design.md
  #   § "Validation Rules (room-service)", rule #3:
  #   "newRole must be RoleOwner or RoleMember | \"invalid role: must be owner or member\""
  # Transport: NATS request/reply on
  #   chat.user.<account>.request.room.<roomID>.<site>.member.role-update
  # The reply is {"status":"accepted"} on success, or {"error":"..."} on
  # failure. Rule #3 is checked before the room-existence/ownership checks,
  # so it is observable without a pre-existing room or membership.

  @smoke @covers:room-roleupdate-invalid-role
  Scenario: Updating a member to an unknown role is rejected as invalid
    Given user "alice" is authenticated
    When "alice" requests role "supervisor" for member "bob" in room "general"
    Then the response is a Validation error
