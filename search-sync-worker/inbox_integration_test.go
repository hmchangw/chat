//go:build integration

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// --- Shared helpers for inbox-based collection integration tests ---

// createInboxStream creates the INBOX_{siteID} stream using pkg/stream.Inbox
// as the canonical baseline (name + local/aggregate subject patterns), with
// no cross-site Sources. Cross-site Sources are a production deployment
// concern owned by inbox-worker; tests simulate federated events by
// publishing directly to the aggregate subject instead.
func createInboxStream(t *testing.T, ctx context.Context, js jetstream.JetStream, siteID string) {
	t.Helper()
	cfg := stream.Inbox(siteID)
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     cfg.Name,
		Subjects: cfg.Subjects,
	})
	require.NoError(t, err, "create INBOX stream for %s", siteID)
}

// memberFixture is one subscription entry in a test member event. Used by
// both the single-sub convenience helper and the bulk-invite helper.
//
// Exactly one of (Restricted, HistorySharedSince) should be used:
//   - Restricted=true is a shortcut: the fixture builder picks a synthetic
//     historyFrom timestamp and sets Subscription.HistorySharedSince to it.
//     Use this when the test only cares about "is this subscription
//     restricted?" and not the exact value of HistorySharedSince.
//   - HistorySharedSince=&t is used verbatim: the test supplies the exact
//     timestamp it wants on the resulting Subscription. Use this when the
//     test asserts on or logs the specific historyFrom value.
//
// If both are set, HistorySharedSince wins. If neither is set, the resulting
// Subscription is unrestricted (HistorySharedSince stays nil).
type memberFixture struct {
	SubID              string
	Account            string
	Restricted         bool
	HistorySharedSince *time.Time
}

// buildMemberEventPayload constructs a single-subscription MemberAddedPayload
// — the common integration-test case where one user joins/leaves one room.
// For bulk invites use buildBulkMemberEventPayload.
func buildMemberEventPayload(
	subID, account, roomID, roomName, siteID string,
	joinedAt time.Time,
	historyShared *time.Time,
) model.MemberAddedPayload {
	return buildBulkMemberEventPayload(roomID, roomName, siteID, joinedAt, []memberFixture{{
		SubID:              subID,
		Account:            account,
		HistorySharedSince: historyShared, // propagate the exact timestamp the caller passed
	}})
}

// buildBulkMemberEventPayload constructs a MemberAddedPayload with N
// subscriptions all targeting the same room — the shape room-worker
// publishes when an admin bulk-invites multiple users in one action.
func buildBulkMemberEventPayload(
	roomID, roomName, siteID string,
	joinedAt time.Time,
	members []memberFixture,
) model.MemberAddedPayload {
	// Synthetic history timestamp used when a fixture sets Restricted=true
	// without supplying an explicit HistorySharedSince. Choosing joinedAt-1h
	// keeps it realistic (history shared from "earlier today") while being
	// deterministic.
	defaultHistoryFrom := joinedAt.Add(-1 * time.Hour)
	subscriptions := make([]model.Subscription, 0, len(members))
	for _, m := range members {
		sub := model.Subscription{
			ID:         m.SubID,
			User:       model.SubscriptionUser{ID: "u-" + m.Account, Account: m.Account},
			RoomID:     roomID,
			SiteID:     siteID,
			Roles:      []model.Role{model.RoleMember},
			JoinedAt:   joinedAt,
			LastSeenAt: joinedAt,
		}
		switch {
		case m.HistorySharedSince != nil:
			sub.HistorySharedSince = m.HistorySharedSince
		case m.Restricted:
			sub.HistorySharedSince = &defaultHistoryFrom
		}
		subscriptions = append(subscriptions, sub)
	}
	return model.MemberAddedPayload{
		Subscriptions: subscriptions,
		Room: model.Room{
			ID:        roomID,
			Name:      roomName,
			Type:      model.RoomTypeGroup,
			CreatedBy: "u-admin",
			SiteID:    siteID,
			CreatedAt: joinedAt,
			UpdatedAt: joinedAt,
		},
	}
}

// publishMemberOutboxEvent wraps a MemberAddedPayload inside an OutboxEvent
// with the given Type + event-level Timestamp and publishes it to `subj`.
// The caller picks `subj` (local vs. aggregate, added vs. removed) via the
// subject builders in pkg/subject.
func publishMemberOutboxEvent(
	t *testing.T,
	ctx context.Context,
	js jetstream.JetStream,
	subj, eventType string,
	payload model.MemberAddedPayload,
	timestamp int64,
) {
	t.Helper()
	payloadData, err := json.Marshal(payload)
	require.NoError(t, err)

	// SiteID/DestSiteID on the envelope come from the Room (all subscriptions
	// in one event target the same room, so they share the same siteID).
	evt := model.OutboxEvent{
		Type:       eventType,
		SiteID:     payload.Room.SiteID,
		DestSiteID: payload.Room.SiteID,
		Payload:    payloadData,
		Timestamp:  timestamp,
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)

	_, err = js.Publish(ctx, subj, data)
	require.NoError(t, err, "publish to %s", subj)
}

// drainConsumer fetches exactly `expected` JetStream messages from the
// consumer and feeds them through the handler (Add + Flush). Fails if fewer
// messages are delivered within the fetch timeout.
func drainConsumer(
	t *testing.T,
	ctx context.Context,
	cons jetstream.Consumer,
	handler *Handler,
	expected int,
) {
	t.Helper()
	if expected == 0 {
		handler.Flush(ctx)
		return
	}

	received := 0
	for attempts := 0; attempts < 5 && received < expected; attempts++ {
		batch, err := cons.Fetch(expected-received, jetstream.FetchMaxWait(5*time.Second))
		require.NoError(t, err)
		for msg := range batch.Messages() {
			handler.Add(msg)
			received++
		}
	}
	require.Equal(t, expected, received, "drained %d of %d expected messages", received, expected)
	handler.Flush(ctx)
}

// toStringSlice converts a JSON-decoded array (`[]any`) to `[]string`.
// Fails the test if any element is not a string.
func toStringSlice(t *testing.T, v any) []string {
	t.Helper()
	if v == nil {
		return nil
	}
	slice, ok := v.([]any)
	require.True(t, ok, "expected []any, got %T", v)
	out := make([]string, 0, len(slice))
	for _, item := range slice {
		s, ok := item.(string)
		require.True(t, ok, "expected string element, got %T", item)
		out = append(out, s)
	}
	return out
}

// --- Spotlight integration test ---

func TestSpotlightSyncIntegration(t *testing.T) {
	esURL := setupElasticsearch(t)
	js, _ := setupNATSJetStream(t)
	ctx := context.Background()

	siteID := "site-spot"
	indexName := "spotlight-site-spot-v1-chat"

	// --- ES template + index ---
	engine, err := searchengine.New(ctx, "elasticsearch", esURL)
	require.NoError(t, err)
	waitForClusterGreen(t, esURL, 120*time.Second)

	coll := newSpotlightCollection(indexName)
	require.NoError(t, engine.UpsertTemplate(ctx, coll.TemplateName(), overrideIndexSettings(spotlightTemplateBody(indexName))))
	preCreateIndex(t, esURL, indexName)
	waitForClusterGreen(t, esURL, 120*time.Second)

	// --- NATS INBOX stream + consumer ---
	createInboxStream(t, ctx, js, siteID)
	cons, err := js.CreateOrUpdateConsumer(ctx, stream.Inbox(siteID).Name, jetstream.ConsumerConfig{
		Durable:        "spotlight-sync-inttest",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: coll.FilterSubjects(siteID),
	})
	require.NoError(t, err, "create spotlight consumer")

	handler := NewHandler(&engineAdapter{engine: engine}, coll, 100)

	joinedAt := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	// --- Publish events covering local + federated + remove ---

	// Local member_added: alice joins engineering
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberAdded(siteID),
		model.OutboxMemberAdded,
		buildMemberEventPayload("sub-alice-eng", "alice", "r-eng", "engineering", siteID, joinedAt, nil),
		1000,
	)

	// Local member_added: alice joins platform
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberAdded(siteID),
		model.OutboxMemberAdded,
		buildMemberEventPayload("sub-alice-platform", "alice", "r-platform", "platform", siteID, joinedAt, nil),
		1100,
	)

	// Federated (aggregate) member_added: bob joins engineering via a cross-site event
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberAddedAggregate(siteID),
		model.OutboxMemberAdded,
		buildMemberEventPayload("sub-bob-eng", "bob", "r-eng", "engineering", siteID, joinedAt, nil),
		1200,
	)

	// Federated (aggregate) member_removed: alice leaves platform
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberRemovedAggregate(siteID),
		model.OutboxMemberRemoved,
		buildMemberEventPayload("sub-alice-platform", "alice", "r-platform", "platform", siteID, joinedAt, nil),
		1300,
	)

	drainConsumer(t, ctx, cons, handler, 4)
	refreshIndex(t, esURL, indexName)

	// --- Verify ---

	t.Run("two subscriptions remain after one removal", func(t *testing.T) {
		// Added 3 (alice-eng, alice-platform, bob-eng), removed 1 (alice-platform)
		assert.Equal(t, 2, countDocs(t, esURL, indexName))
	})

	t.Run("alice engineering doc shape", func(t *testing.T) {
		doc := getDoc(t, esURL, indexName, "sub-alice-eng")
		require.NotNil(t, doc, "sub-alice-eng should be indexed")
		assert.Equal(t, "sub-alice-eng", doc["subscriptionId"])
		assert.Equal(t, "u-alice", doc["userId"])
		assert.Equal(t, "alice", doc["userAccount"])
		assert.Equal(t, "r-eng", doc["roomId"])
		assert.Equal(t, "engineering", doc["roomName"])
		assert.Equal(t, "group", doc["roomType"])
		assert.Equal(t, siteID, doc["siteId"])
	})

	t.Run("federated bob doc was indexed", func(t *testing.T) {
		doc := getDoc(t, esURL, indexName, "sub-bob-eng")
		require.NotNil(t, doc, "bob's federated subscription should be indexed via aggregate filter")
		assert.Equal(t, "bob", doc["userAccount"])
		assert.Equal(t, "r-eng", doc["roomId"])
	})

	t.Run("removed alice-platform doc is gone", func(t *testing.T) {
		doc := getDoc(t, esURL, indexName, "sub-alice-platform")
		assert.Nil(t, doc, "removed subscription should not exist in the index")
	})
}

// TestSpotlightSync_BulkInvite verifies the fan-out path end-to-end: a single
// JetStream message carrying N subscriptions must produce N spotlight docs
// in one ES bulk request.
func TestSpotlightSync_BulkInvite(t *testing.T) {
	esURL := setupElasticsearch(t)
	js, _ := setupNATSJetStream(t)
	ctx := context.Background()

	siteID := "site-spot-bulk"
	indexName := "spotlight-site-spot-bulk-v1-chat"

	engine, err := searchengine.New(ctx, "elasticsearch", esURL)
	require.NoError(t, err)
	waitForClusterGreen(t, esURL, 120*time.Second)

	coll := newSpotlightCollection(indexName)
	require.NoError(t, engine.UpsertTemplate(ctx, coll.TemplateName(), overrideIndexSettings(spotlightTemplateBody(indexName))))
	preCreateIndex(t, esURL, indexName)
	waitForClusterGreen(t, esURL, 120*time.Second)

	createInboxStream(t, ctx, js, siteID)
	cons, err := js.CreateOrUpdateConsumer(ctx, stream.Inbox(siteID).Name, jetstream.ConsumerConfig{
		Durable:        "spotlight-sync-bulk-inttest",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: coll.FilterSubjects(siteID),
	})
	require.NoError(t, err, "create spotlight consumer")

	handler := NewHandler(&engineAdapter{engine: engine}, coll, 100)

	joinedAt := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	// One bulk-invite event adds 3 users to r-platform at once.
	payload := buildBulkMemberEventPayload("r-platform", "platform", siteID, joinedAt, []memberFixture{
		{SubID: "sub-dave-platform", Account: "dave"},
		{SubID: "sub-erin-platform", Account: "erin"},
		{SubID: "sub-frank-platform", Account: "frank"},
	})
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberAdded(siteID),
		model.OutboxMemberAdded,
		payload,
		5000,
	)

	// Only ONE JetStream message is drained, but it produces THREE ES index
	// actions — handler.ActionCount() > MessageCount() is the whole point
	// of the fan-out path.
	drainConsumer(t, ctx, cons, handler, 1)
	refreshIndex(t, esURL, indexName)

	t.Run("all three subscriptions landed in the index", func(t *testing.T) {
		assert.Equal(t, 3, countDocs(t, esURL, indexName),
			"1 message × 3 subscriptions = 3 spotlight docs")
	})

	t.Run("each subscription has the correct doc shape", func(t *testing.T) {
		for _, sub := range []struct {
			docID   string
			account string
		}{
			{"sub-dave-platform", "dave"},
			{"sub-erin-platform", "erin"},
			{"sub-frank-platform", "frank"},
		} {
			doc := getDoc(t, esURL, indexName, sub.docID)
			require.NotNil(t, doc, "%s should be indexed", sub.docID)
			assert.Equal(t, sub.docID, doc["subscriptionId"])
			assert.Equal(t, sub.account, doc["userAccount"])
			assert.Equal(t, "r-platform", doc["roomId"])
			assert.Equal(t, "platform", doc["roomName"])
		}
	})

	t.Run("bulk remove evicts all three docs", func(t *testing.T) {
		// Same 3 subscriptions, now removed in one event.
		publishMemberOutboxEvent(t, ctx, js,
			subject.InboxMemberRemoved(siteID),
			model.OutboxMemberRemoved,
			payload,
			6000,
		)
		drainConsumer(t, ctx, cons, handler, 1)
		refreshIndex(t, esURL, indexName)

		assert.Equal(t, 0, countDocs(t, esURL, indexName),
			"1 message × 3 subscription deletes = 0 docs remaining")
	})
}

// --- User-room integration test ---

func TestUserRoomSyncIntegration(t *testing.T) {
	esURL := setupElasticsearch(t)
	js, _ := setupNATSJetStream(t)
	ctx := context.Background()

	siteID := "site-ur"
	indexName := "user-room-site-ur"

	engine, err := searchengine.New(ctx, "elasticsearch", esURL)
	require.NoError(t, err)
	waitForClusterGreen(t, esURL, 120*time.Second)

	coll := newUserRoomCollection(indexName)
	require.NoError(t, engine.UpsertTemplate(ctx, coll.TemplateName(), overrideIndexSettings(userRoomTemplateBody(indexName))))
	preCreateIndex(t, esURL, indexName)
	waitForClusterGreen(t, esURL, 120*time.Second)

	createInboxStream(t, ctx, js, siteID)
	cons, err := js.CreateOrUpdateConsumer(ctx, stream.Inbox(siteID).Name, jetstream.ConsumerConfig{
		Durable:        "user-room-sync-inttest",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: coll.FilterSubjects(siteID),
	})
	require.NoError(t, err, "create user-room consumer")

	handler := NewHandler(&engineAdapter{engine: engine}, coll, 100)

	joinedAt := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	// --- Publish ---

	// alice joins 3 rooms via local events
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberAdded(siteID), model.OutboxMemberAdded,
		buildMemberEventPayload("sub-alice-r1", "alice", "r1", "general", siteID, joinedAt, nil),
		1000,
	)
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberAdded(siteID), model.OutboxMemberAdded,
		buildMemberEventPayload("sub-alice-r2", "alice", "r2", "random", siteID, joinedAt, nil),
		1100,
	)
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberAdded(siteID), model.OutboxMemberAdded,
		buildMemberEventPayload("sub-alice-r3", "alice", "r3", "eng", siteID, joinedAt, nil),
		1200,
	)

	// bob joins r1 via a federated (aggregate) event
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberAddedAggregate(siteID), model.OutboxMemberAdded,
		buildMemberEventPayload("sub-bob-r1", "bob", "r1", "general", siteID, joinedAt, nil),
		1300,
	)

	// alice leaves r2 via a local event
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberRemoved(siteID), model.OutboxMemberRemoved,
		buildMemberEventPayload("sub-alice-r2", "alice", "r2", "random", siteID, joinedAt, nil),
		1400,
	)

	// alice joins a restricted room → user-room-sync should SKIP this event
	// (no doc write). It still counts as a received message at the consumer
	// level — the handler acks filtered events without buffering them.
	restrictedFrom := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberAdded(siteID), model.OutboxMemberAdded,
		buildMemberEventPayload("sub-alice-restricted", "alice", "r-restricted", "archives", siteID, joinedAt, &restrictedFrom),
		1500,
	)

	drainConsumer(t, ctx, cons, handler, 6)
	refreshIndex(t, esURL, indexName)

	// --- Verify ---

	t.Run("alice rooms reflect adds and one remove", func(t *testing.T) {
		doc := getDoc(t, esURL, indexName, "alice")
		require.NotNil(t, doc, "alice user-room doc should exist")
		rooms := toStringSlice(t, doc["rooms"])
		assert.ElementsMatch(t, []string{"r1", "r3"}, rooms,
			"alice should be in r1, r3 (r2 removed, r-restricted skipped)")
	})

	t.Run("bob created via federated event", func(t *testing.T) {
		doc := getDoc(t, esURL, indexName, "bob")
		require.NotNil(t, doc, "bob should be upserted from aggregate.member_added")
		rooms := toStringSlice(t, doc["rooms"])
		assert.ElementsMatch(t, []string{"r1"}, rooms)
	})

	t.Run("roomTimestamps retained after remove", func(t *testing.T) {
		doc := getDoc(t, esURL, indexName, "alice")
		require.NotNil(t, doc)
		rts, ok := doc["roomTimestamps"].(map[string]any)
		require.True(t, ok, "roomTimestamps should be a flattened map")

		// r1, r2, r3 all get their last-seen timestamps stored. r2's
		// entry is KEPT after the remove (preserves LWW monotonicity so a
		// late-arriving stale add can't re-insert r2).
		assert.Equal(t, float64(1000), rts["r1"])
		assert.Equal(t, float64(1400), rts["r2"],
			"r2 timestamp should be bumped to the remove's event timestamp, not deleted")
		assert.Equal(t, float64(1200), rts["r3"])

		// Restricted room was never applied → no timestamp entry.
		assert.NotContains(t, rts, "r-restricted")
	})

	t.Run("restricted room not present in rooms array", func(t *testing.T) {
		doc := getDoc(t, esURL, indexName, "alice")
		require.NotNil(t, doc)
		rooms := toStringSlice(t, doc["rooms"])
		assert.NotContains(t, rooms, "r-restricted",
			"restricted rooms are skipped by user-room-sync")
	})

	t.Run("createdAt and updatedAt stamped from upsert", func(t *testing.T) {
		doc := getDoc(t, esURL, indexName, "alice")
		require.NotNil(t, doc)
		assert.NotEmpty(t, doc["createdAt"], "upsert should seed createdAt")
		assert.NotEmpty(t, doc["updatedAt"], "add path should stamp updatedAt")
	})
}

// TestUserRoomSync_BulkInvite verifies the fan-out path for user-room: a
// single JetStream message with N subscriptions produces N distinct user-room
// updates (different DocIDs since each sub targets a different user),
// with per-subscription restricted-room filtering.
func TestUserRoomSync_BulkInvite(t *testing.T) {
	esURL := setupElasticsearch(t)
	js, _ := setupNATSJetStream(t)
	ctx := context.Background()

	siteID := "site-ur-bulk"
	indexName := "user-room-site-ur-bulk"

	engine, err := searchengine.New(ctx, "elasticsearch", esURL)
	require.NoError(t, err)
	waitForClusterGreen(t, esURL, 120*time.Second)

	coll := newUserRoomCollection(indexName)
	require.NoError(t, engine.UpsertTemplate(ctx, coll.TemplateName(), overrideIndexSettings(userRoomTemplateBody(indexName))))
	preCreateIndex(t, esURL, indexName)
	waitForClusterGreen(t, esURL, 120*time.Second)

	createInboxStream(t, ctx, js, siteID)
	cons, err := js.CreateOrUpdateConsumer(ctx, stream.Inbox(siteID).Name, jetstream.ConsumerConfig{
		Durable:        "user-room-sync-bulk-inttest",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: coll.FilterSubjects(siteID),
	})
	require.NoError(t, err, "create user-room consumer")

	handler := NewHandler(&engineAdapter{engine: engine}, coll, 100)

	joinedAt := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	// One bulk-invite event: 4 users to r-platform, of which 2 are
	// restricted (should be filtered out per-subscription). Only 2 user-room
	// docs should be created.
	payload := buildBulkMemberEventPayload("r-platform", "platform", siteID, joinedAt, []memberFixture{
		{SubID: "sub-dave", Account: "dave", Restricted: false},
		{SubID: "sub-erin", Account: "erin", Restricted: true}, // skip
		{SubID: "sub-frank", Account: "frank", Restricted: false},
		{SubID: "sub-gina", Account: "gina", Restricted: true}, // skip
	})
	publishMemberOutboxEvent(t, ctx, js,
		subject.InboxMemberAdded(siteID),
		model.OutboxMemberAdded,
		payload,
		5000,
	)

	// One JetStream message, 2 fan-out actions (4 subscriptions × 2
	// unrestricted = 2 upserts).
	drainConsumer(t, ctx, cons, handler, 1)
	refreshIndex(t, esURL, indexName)

	t.Run("only unrestricted users were upserted", func(t *testing.T) {
		// dave and frank present
		daveDoc := getDoc(t, esURL, indexName, "dave")
		require.NotNil(t, daveDoc, "dave should be upserted")
		daveRooms := toStringSlice(t, daveDoc["rooms"])
		assert.ElementsMatch(t, []string{"r-platform"}, daveRooms)

		frankDoc := getDoc(t, esURL, indexName, "frank")
		require.NotNil(t, frankDoc, "frank should be upserted")
		frankRooms := toStringSlice(t, frankDoc["rooms"])
		assert.ElementsMatch(t, []string{"r-platform"}, frankRooms)

		// erin and gina absent — restricted rooms are filtered per-sub
		assert.Nil(t, getDoc(t, esURL, indexName, "erin"),
			"restricted erin must not be upserted")
		assert.Nil(t, getDoc(t, esURL, indexName, "gina"),
			"restricted gina must not be upserted")
	})

	t.Run("bulk remove on mixed set only evicts from unrestricted users", func(t *testing.T) {
		// Same 4-user event, now a remove. Only dave and frank have docs
		// to update; erin and gina are still skipped.
		publishMemberOutboxEvent(t, ctx, js,
			subject.InboxMemberRemoved(siteID),
			model.OutboxMemberRemoved,
			payload,
			6000,
		)
		drainConsumer(t, ctx, cons, handler, 1)
		refreshIndex(t, esURL, indexName)

		daveDoc := getDoc(t, esURL, indexName, "dave")
		require.NotNil(t, daveDoc, "dave's user doc should still exist (ghost)")
		assert.Empty(t, toStringSlice(t, daveDoc["rooms"]),
			"dave's rooms array should be empty after bulk remove")

		frankDoc := getDoc(t, esURL, indexName, "frank")
		require.NotNil(t, frankDoc)
		assert.Empty(t, toStringSlice(t, frankDoc["rooms"]))

		// erin and gina still absent — the remove is a no-op for restricted
		// subs so no doc gets created.
		assert.Nil(t, getDoc(t, esURL, indexName, "erin"))
		assert.Nil(t, getDoc(t, esURL, indexName, "gina"))
	})
}

// --- User-room LWW guard integration test ---

// TestUserRoomSync_LWWGuard drives a single user doc through a sequence of
// in-order and out-of-order events to prove the per-room timestamp guard
// converges on highest-event-timestamp-wins state regardless of physical
// arrival order.
//
// Implemented as one linear test body (not split into t.Run subtests)
// because the scenario is inherently stateful — each step depends on the
// prior ES state. Splitting into independent subtests would require
// rebuilding the doc from scratch for each step, defeating the purpose of
// testing the guard's monotonicity across a realistic sequence.
func TestUserRoomSync_LWWGuard(t *testing.T) {
	esURL := setupElasticsearch(t)
	js, _ := setupNATSJetStream(t)
	ctx := context.Background()

	siteID := "site-lww"
	indexName := "user-room-site-lww"

	engine, err := searchengine.New(ctx, "elasticsearch", esURL)
	require.NoError(t, err)
	waitForClusterGreen(t, esURL, 120*time.Second)

	coll := newUserRoomCollection(indexName)
	require.NoError(t, engine.UpsertTemplate(ctx, coll.TemplateName(), overrideIndexSettings(userRoomTemplateBody(indexName))))
	preCreateIndex(t, esURL, indexName)
	waitForClusterGreen(t, esURL, 120*time.Second)

	createInboxStream(t, ctx, js, siteID)
	cons, err := js.CreateOrUpdateConsumer(ctx, stream.Inbox(siteID).Name, jetstream.ConsumerConfig{
		Durable:        "user-room-sync-lww-inttest",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: coll.FilterSubjects(siteID),
	})
	require.NoError(t, err)

	handler := NewHandler(&engineAdapter{engine: engine}, coll, 100)

	joinedAt := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	publish := func(subj, eventType, subID, roomID string, ts int64) {
		publishMemberOutboxEvent(t, ctx, js, subj, eventType,
			buildMemberEventPayload(subID, "charlie", roomID, "room "+roomID, siteID, joinedAt, nil),
			ts,
		)
	}

	// getCharlieState reads charlie's user-room doc from ES and returns the
	// (rooms, roomTimestamps) tuple after refreshing the index.
	getCharlieState := func() ([]string, map[string]any) {
		refreshIndex(t, esURL, indexName)
		doc := getDoc(t, esURL, indexName, "charlie")
		require.NotNil(t, doc, "charlie user-room doc should exist")
		rooms := toStringSlice(t, doc["rooms"])
		rts, _ := doc["roomTimestamps"].(map[string]any)
		return rooms, rts
	}

	// Step 1: initial add at ts=2000 creates the doc via upsert.
	publish(subject.InboxMemberAdded(siteID), model.OutboxMemberAdded, "sub-c-rA", "rA", 2000)
	drainConsumer(t, ctx, cons, handler, 1)
	rooms, rts := getCharlieState()
	assert.Contains(t, rooms, "rA", "step 1: initial add should put rA in rooms")
	assert.Equal(t, float64(2000), rts["rA"], "step 1: stored ts should be 2000")

	// Step 2: stale add at ts=1000 should be a no-op via ctx.op='none'.
	// Same (user, room) but with an older event timestamp; the guard
	// inside the painless script must set ctx.op='none' and skip the write.
	publish(subject.InboxMemberAdded(siteID), model.OutboxMemberAdded, "sub-c-rA", "rA", 1000)
	drainConsumer(t, ctx, cons, handler, 1)
	rooms, rts = getCharlieState()
	assert.Contains(t, rooms, "rA", "step 2: rA should still be in rooms after stale add")
	assert.Equal(t, float64(2000), rts["rA"],
		"step 2: stale add must not overwrite a newer stored timestamp")

	// Step 3: stale remove at ts=1500 should also be a no-op.
	// Remove arrives out of order with an older timestamp than the prior
	// add (2000). Guard rejects it — rA stays.
	publish(subject.InboxMemberRemoved(siteID), model.OutboxMemberRemoved, "sub-c-rA", "rA", 1500)
	drainConsumer(t, ctx, cons, handler, 1)
	rooms, rts = getCharlieState()
	assert.Contains(t, rooms, "rA", "step 3: rA should survive stale remove")
	assert.Equal(t, float64(2000), rts["rA"], "step 3: stored timestamp unchanged after stale remove")

	// Step 4: newer remove at ts=3000 evicts the room.
	// Stored ts bumps to the remove's ts so any later stale add below 3000
	// is also rejected.
	publish(subject.InboxMemberRemoved(siteID), model.OutboxMemberRemoved, "sub-c-rA", "rA", 3000)
	drainConsumer(t, ctx, cons, handler, 1)
	rooms, rts = getCharlieState()
	assert.NotContains(t, rooms, "rA", "step 4: newer remove should evict rA")
	assert.Equal(t, float64(3000), rts["rA"],
		"step 4: remove must bump stored timestamp to the remove's ts")

	// Step 5: re-add with newer ts=4000 puts the room back.
	// Simulates the user re-joining after being removed.
	publish(subject.InboxMemberAdded(siteID), model.OutboxMemberAdded, "sub-c-rA-v2", "rA", 4000)
	drainConsumer(t, ctx, cons, handler, 1)
	rooms, rts = getCharlieState()
	assert.Contains(t, rooms, "rA", "step 5: re-add with newer ts should restore rA")
	assert.Equal(t, float64(4000), rts["rA"], "step 5: stored ts should bump to 4000")

	// Step 6: stale add at ts=2500 after the re-add is still a no-op.
	// Guard continues to work after the re-add cycle. rA must remain
	// exactly once in the array (no duplicate from the stale add attempting
	// to re-insert).
	publish(subject.InboxMemberAdded(siteID), model.OutboxMemberAdded, "sub-c-rA", "rA", 2500)
	drainConsumer(t, ctx, cons, handler, 1)
	rooms, rts = getCharlieState()
	assert.Contains(t, rooms, "rA", "step 6: rA should still be present")
	assert.Equal(t, float64(4000), rts["rA"], "step 6: stored ts unchanged after stale add")
	rACount := 0
	for _, r := range rooms {
		if r == "rA" {
			rACount++
		}
	}
	assert.Equal(t, 1, rACount, "step 6: rA should not be duplicated by stale add")
}
