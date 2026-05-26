Feature: Messaging pipeline — message submission validation
  # Source: docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md
  #   § "Error Handling" / § "Processing Flow":
  #     - invalid message ID  -> reply error, ack
  #     - empty content       -> reply error, ack
  # Source: tools/integration-suite/README.md § "Status & maturity"
  #   (Part 2 will add JetStream consumer/peek primitives).
  #
  # message-gatekeeper consumes a fire-and-forget JetStream publish on
  # MESSAGES_<site> and replies on a *decoupled* subject
  # chat.user.<account>.response.<requestId> (no request/reply
  # correlation). The Part-1 suite has only HTTP and NATS request/reply
  # primitives, so the gatekeeper's documented validation contract is not
  # observable yet. These scenarios capture the documented expected
  # behavior and are tagged @blindspot until the Part-2 JetStream
  # primitive exists; they fail by design (blindspots-as-failures).

  @blindspot:message-submit-needs-jetstream-primitive @covers:messaging-submit-empty-content
  Scenario: Submitting a message with an empty body is rejected
    Given user "alice" is authenticated
    When "alice" submits a message with an empty body
    Then the response is a Validation error

  @blindspot:message-submit-needs-jetstream-primitive @covers:messaging-submit-invalid-id
  Scenario: Submitting a message with an invalid message id is rejected
    Given user "alice" is authenticated
    When "alice" submits a message with an invalid message id
    Then the response is a Validation error
