package integrationsuite

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

// registerMessagePersistenceSteps registers all Part-2 step stubs for the
// message-persistence pipeline feature. These steps require JetStream
// publish/peek, Cassandra query, MongoDB query, or fault-injection primitives
// that are not yet available in Part-1. Each stub returns a structured error
// so failing scenarios are self-explanatory in last-run.md.
func registerMessagePersistenceSteps(ctx *godog.ScenarioContext) {
	// ── Messaging — JetStream observation (steps 56–63) ──────────────────────

	// Step 56: canonical created event on MESSAGES_CANONICAL within deadline.
	ctx.Step(`^within ([0-9]+)s a canonical created event exists for the message in room "([^"]+)"$`,
		withinSecondsCanonicalCreatedEventExists)

	// Step 57: publish canonical created event to MESSAGES_CANONICAL.
	ctx.Step(`^a canonical created event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$`,
		canonicalCreatedEventPublished)

	// Step 58: publish canonical created event for a thread reply.
	ctx.Step(`^a canonical created event for thread reply "([^"]+)" by "([^"]+)" to parent "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$`,
		canonicalCreatedThreadReplyEventPublished)

	// Step 59: publish canonical created event twice (redelivery simulation).
	ctx.Step(`^the canonical created event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL twice$`,
		canonicalCreatedEventPublishedTwice)

	// Step 60: publish two canonical created events out of order.
	ctx.Step(`^canonical created events for "([^"]+)" \(created ([^)]+)\) and "([^"]+)" \(created ([^)]+)\) are delivered out of order to MESSAGES_CANONICAL$`,
		canonicalCreatedEventsOutOfOrder)

	// Step 61: publish canonical updated event to MESSAGES_CANONICAL.
	ctx.Step(`^a canonical updated event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$`,
		canonicalUpdatedEventPublished)

	// Step 62: publish canonical deleted event to MESSAGES_CANONICAL.
	ctx.Step(`^a canonical deleted event for message "([^"]+)" by "([^"]+)" in room "([^"]+)" is published to MESSAGES_CANONICAL$`,
		canonicalDeletedEventPublished)

	// Step 63: cross-site canonical created event for thread reply.
	ctx.Step(`^a canonical created event for thread reply "([^"]+)" by "([^"]+)" \(home site "([^"]+)"\) to parent "([^"]+)" in room "([^"]+)" on site "([^"]+)" is published to MESSAGES_CANONICAL$`,
		canonicalCreatedCrossSiteThreadReplyPublished)

	// ── Cassandra observation (steps 76–82) ──────────────────────────────────

	// Step 76: Cassandra table contains row for message in room (with optional "within Ns" prefix).
	ctx.Step(`^(?:within \d+s )?Cassandra table "([^"]+)" contains a row for message "([^"]+)" in room "([^"]+)"$`,
		cassandraTableContainsRowForMessageInRoom)

	// Step 77: Cassandra table contains row for message (by message ID only).
	ctx.Step(`^(?:within \d+s )?Cassandra table "([^"]+)" contains a row for message "([^"]+)"$`,
		cassandraTableContainsRowForMessage)

	// Step 78: assert bucket column equals expected time-window value.
	ctx.Step(`^(?:within \d+s )?the "([^"]+)" row for message "([^"]+)" has bucket equal to the expected time-window value for its created_at$`,
		cassandraRowBucketEqualsExpectedTimeWindow)

	// Step 79: assert tcount value in Cassandra table.
	ctx.Step(`^(?:within \d+s )?the tcount for message "([^"]+)" in "([^"]+)" is (\d+)$`,
		cassandraTCountForMessage)

	// Step 80: assert exact row count in Cassandra table (optional "within Ns" prefix).
	ctx.Step(`^(?:within \d+s )?exactly (\d+) row for message "([^"]+)" exists in Cassandra table "([^"]+)"$`,
		cassandraExactRowCount)

	// Step 81: assert message-worker consumer sequence did not advance.
	ctx.Step(`^(?:within \d+s )?the message-worker durable consumer sequence does not advance for the (updated|deleted) event$`,
		cassandraConsumerSequenceDoesNotAdvance)

	// Step 82: assert delivery count on message-worker consumer.
	ctx.Step(`^(?:within \d+s )?the delivery count for message "([^"]+)" on the message-worker consumer is greater than (\d+)$`,
		cassandraDeliveryCountGreaterThan)

	// ── MongoDB observation (steps 83–85) ─────────────────────────────────────

	// Step 83: MongoDB collection contains document with parentMessageId.
	ctx.Step(`^(?:within \d+s )?MongoDB collection "([^"]+)" contains a document with parentMessageId "([^"]+)"$`,
		mongoDBContainsDocumentWithParentMessageID)

	// Step 84: MongoDB collection contains document for user and threadParent.
	ctx.Step(`^(?:within \d+s )?MongoDB collection "([^"]+)" contains a document for user "([^"]+)" and threadParent "([^"]+)"$`,
		mongoDBContainsDocumentForUserAndThreadParent)

	// Step 85: OUTBOX event appears on the OUTBOX stream.
	ctx.Step(`^(?:within \d+s )?an OUTBOX event of type "([^"]+)" destined for site "([^"]+)" appears on the OUTBOX stream$`,
		outboxEventAppearsOnStream)

	// Step 85 (room-member-ops phrasing): outbox event published to site.
	ctx.Step(`^within (\d+)s an outbox event of type "([^"]+)" is published to site "([^"]+)"$`,
		withinSecondsOutboxEventPublishedToSite)

	// ── Fault injection (steps 86–87) ─────────────────────────────────────────

	// Step 86: Cassandra made unavailable.
	ctx.Step(`^Cassandra is made unavailable$`, cassandraIsUnavailable)

	// Step 87: Cassandra seed fixture — parent message already persisted.
	ctx.Step(`^message "([^"]+)" by "([^"]+)" in room "([^"]+)" already persisted in Cassandra$`,
		messageAlreadyPersistedInCassandra)

	// ── Extra steps not in steps-final.md but used in feature files ───────────

	// "user X from site Y is authenticated" — multi-site auth used in
	// message-persistence.feature. Delegates to userIsAuthenticatedInSite.
	ctx.Step(`^user "([^"]+)" from site "([^"]+)" is authenticated$`,
		userFromSiteIsAuthenticated)

	// Cassandra bucket existence steps used in message-persistence.feature
	// (out-of-order delivery scenario). Part-2 Cassandra primitive required.
	ctx.Step(`^(?:within \d+s )?"([^"]+)" exists in Cassandra in the bucket for (\d+) hours? ago$`,
		existsInCassandraInBucketForHoursAgo)
	ctx.Step(`^(?:within \d+s )?"([^"]+)" exists in Cassandra in the bucket for the current window$`,
		existsInCassandraInBucketForCurrentWindow)
}

// ── JetStream observation stubs ──────────────────────────────────────────────

func withinSecondsCanonicalCreatedEventExists(_ context.Context, deadline int, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func canonicalCreatedEventPublished(_ context.Context, messageID, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func canonicalCreatedThreadReplyEventPublished(_ context.Context, messageID, actor, parentID, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func canonicalCreatedEventPublishedTwice(_ context.Context, messageID, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func canonicalCreatedEventsOutOfOrder(_ context.Context, msgA, createdA, msgB, createdB string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func canonicalUpdatedEventPublished(_ context.Context, messageID, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func canonicalDeletedEventPublished(_ context.Context, messageID, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func canonicalCreatedCrossSiteThreadReplyPublished(_ context.Context, messageID, actor, homeSite, parentID, roomName, site string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

// ── Cassandra observation stubs ───────────────────────────────────────────────

func cassandraTableContainsRowForMessageInRoom(_ context.Context, table, messageID, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: cassandra-observation")
}

func cassandraTableContainsRowForMessage(_ context.Context, table, messageID string) error {
	return fmt.Errorf("part-2 primitive missing: cassandra-observation")
}

func cassandraRowBucketEqualsExpectedTimeWindow(_ context.Context, table, messageID string) error {
	return fmt.Errorf("part-2 primitive missing: cassandra-observation")
}

func cassandraTCountForMessage(_ context.Context, messageID, table string, count int) error {
	return fmt.Errorf("part-2 primitive missing: cassandra-observation")
}

func cassandraExactRowCount(_ context.Context, count int, messageID, table string) error {
	return fmt.Errorf("part-2 primitive missing: cassandra-observation")
}

func cassandraConsumerSequenceDoesNotAdvance(_ context.Context, eventType string) error {
	return fmt.Errorf("part-2 primitive missing: cassandra-observation")
}

func cassandraDeliveryCountGreaterThan(_ context.Context, messageID string, n int) error {
	return fmt.Errorf("part-2 primitive missing: cassandra-observation")
}

// ── MongoDB observation stubs ─────────────────────────────────────────────────

func mongoDBContainsDocumentWithParentMessageID(_ context.Context, collection, parentMessageID string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-observation")
}

func mongoDBContainsDocumentForUserAndThreadParent(_ context.Context, collection, user, threadParent string) error {
	return fmt.Errorf("part-2 primitive missing: mongo-observation")
}

func outboxEventAppearsOnStream(_ context.Context, eventType, destSite string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

// ── Fault injection stubs ─────────────────────────────────────────────────────

func cassandraIsUnavailable(_ context.Context) error {
	return fmt.Errorf("part-2 primitive missing: fault-injection")
}

func messageAlreadyPersistedInCassandra(_ context.Context, messageID, actor, roomName string) error {
	return fmt.Errorf("part-2 primitive missing: cassandra-observation")
}

// ── Extra step implementations ────────────────────────────────────────────────

func withinSecondsOutboxEventPublishedToSite(_ context.Context, deadline int, eventType, site string) error {
	return fmt.Errorf("part-2 primitive missing: jetstream-observation")
}

func userFromSiteIsAuthenticated(ctx context.Context, name, site string) error {
	return userIsAuthenticatedInSite(ctx, name, site)
}

func existsInCassandraInBucketForHoursAgo(_ context.Context, messageID string, hours int) error {
	return fmt.Errorf("part-2 primitive missing: cassandra-observation")
}

func existsInCassandraInBucketForCurrentWindow(_ context.Context, messageID string) error {
	return fmt.Errorf("part-2 primitive missing: cassandra-observation")
}
