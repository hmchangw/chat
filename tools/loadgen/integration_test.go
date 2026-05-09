//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

// setupNATS starts a JetStream-enabled NATS container via the generic
// testcontainers interface (no dedicated NATS module is required).
func setupNATS(t *testing.T) (string, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testimages.NATS,
			Cmd:          []string{"-js"},
			ExposedPorts: []string{"4222/tcp"},
			WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "4222")
	require.NoError(t, err)
	return fmt.Sprintf("nats://%s:%s", host, port.Port()), func() { _ = c.Terminate(ctx) }
}

func setupMongo(t *testing.T) (string, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := mongodb.Run(ctx, testimages.Mongo)
	require.NoError(t, err)
	uri, err := c.ConnectionString(ctx)
	require.NoError(t, err)
	return uri, func() { _ = c.Terminate(ctx) }
}

// TestLoadgenSmallPreset_EndToEnd verifies the generator publishes messages,
// a fake gatekeeper forwards them to MESSAGES_CANONICAL, two JetStream
// consumers drain the stream, a fake broadcast-worker emits room events,
// and MongoDB shows the seeded room data.
func TestLoadgenSmallPreset_EndToEnd(t *testing.T) {
	ctx := context.Background()
	natsURI, stopNATS := setupNATS(t)
	defer stopNATS()
	mongoURI, stopMongo := setupMongo(t)
	defer stopMongo()

	nc, err := nats.Connect(natsURI)
	require.NoError(t, err)
	defer nc.Drain()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	siteID := "site-test"
	canonical := stream.MessagesCanonical(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     canonical.Name,
		Subjects: canonical.Subjects,
	})
	require.NoError(t, err)

	// Two durable consumers that simply ack — stand in for message-worker
	// and broadcast-worker so the canonical stream drains to zero.
	stubConsumers := make([]jetstream.ConsumeContext, 0, 2)
	for _, durable := range []string{"message-worker", "broadcast-worker"} {
		cons, err := js.CreateOrUpdateConsumer(ctx, canonical.Name, jetstream.ConsumerConfig{
			Durable:   durable,
			AckPolicy: jetstream.AckExplicitPolicy,
		})
		require.NoError(t, err)
		cc, err := cons.Consume(func(msg jetstream.Msg) { _ = msg.Ack() })
		require.NoError(t, err)
		stubConsumers = append(stubConsumers, cc)
	}
	defer func() {
		for _, cc := range stubConsumers {
			cc.Stop()
		}
	}()

	// Connect Mongo and seed fixtures.
	client, err := mongoutil.Connect(ctx, mongoURI, "", "")
	require.NoError(t, err)
	defer mongoutil.Disconnect(ctx, client)
	db := client.Database("chat")

	preset, _ := BuiltinPreset("small")
	fixtures := BuildFixtures(&preset, 42, siteID)
	require.NoError(t, Seed(ctx, db, fixtures))

	metrics := NewMetrics()
	collector := NewCollector(metrics, preset.Name)

	// Fake gatekeeper: frontdoor subject → publish MessageEvent to canonical.
	gkSub, err := nc.Subscribe(
		subject.MsgSendWildcard(siteID),
		func(m *nats.Msg) {
			var req model.SendMessageRequest
			if err := json.Unmarshal(m.Data, &req); err != nil {
				return
			}
			evt := model.MessageEvent{
				Message: model.Message{
					ID:        req.ID,
					Content:   req.Content,
					CreatedAt: time.Now().UTC(),
				},
				SiteID:    siteID,
				Timestamp: time.Now().UnixMilli(),
			}
			data, _ := json.Marshal(evt)
			_, _ = js.Publish(ctx, subject.MsgCanonicalCreated(siteID), data)
		},
	)
	require.NoError(t, err)
	defer gkSub.Unsubscribe()

	// Fake broadcast-worker: canonical event → room event.
	bwSub, err := nc.Subscribe(
		subject.MsgCanonicalCreated(siteID),
		func(m *nats.Msg) {
			var evt model.MessageEvent
			if err := json.Unmarshal(m.Data, &evt); err != nil {
				return
			}
			roomEvt := model.RoomEvent{
				Type:    model.RoomEventNewMessage,
				RoomID:  "r",
				Message: &model.ClientMessage{Message: evt.Message},
			}
			data, _ := json.Marshal(roomEvt)
			_ = nc.Publish("chat.room.r.event", data)
		},
	)
	require.NoError(t, err)
	defer bwSub.Unsubscribe()

	publisher := &natsCorePublisher{pool: &ConnPool{observer: nc}}
	gen := NewGenerator(&GeneratorConfig{
		Preset:    &preset,
		Fixtures:  fixtures,
		SiteID:    siteID,
		Rate:      50,
		Inject:    InjectFrontdoor,
		Publisher: publisher,
		Metrics:   metrics,
		Collector: collector,
	}, 42)

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, gen.Run(runCtx))

	// Allow trailing events to flow.
	time.Sleep(2 * time.Second)

	// Assert the canonical stream drained.
	for _, durable := range []string{"message-worker", "broadcast-worker"} {
		cons, err := js.Consumer(ctx, canonical.Name, durable)
		require.NoError(t, err)
		info, err := cons.Info(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(0), info.NumPending, "durable %s still has pending", durable)
	}

	// Assert seed data is visible in Mongo.
	var room model.Room
	err = db.Collection("rooms").FindOne(ctx, bson.M{"_id": fixtures.Rooms[0].ID}).Decode(&room)
	require.NoError(t, err)
	require.Equal(t, fixtures.Rooms[0].ID, room.ID)
}

// TestHistoryReadScenario_EndToEnd_StubService runs HistoryReadGenerator
// against a fake history-service handler that always replies success.
// Proves the wire contract end-to-end via real NATS without requiring
// the actual Cassandra-backed history-service to come up.
func TestHistoryReadScenario_EndToEnd_StubService(t *testing.T) {
	ctx := context.Background()
	natsURI, stopNATS := setupNATS(t)
	defer stopNATS()

	nc, err := nats.Connect(natsURI)
	require.NoError(t, err)
	defer nc.Drain()

	siteID := "site-test"
	preset, _ := BuiltinPreset("history-read")
	fixtures := BuildFixtures(&preset, 42, siteID)

	// Fake history-service: reply to every msg.history / get / surrounding /
	// thread subject with an empty success body.
	wildcards := []string{
		"chat.user.*.request.room.*." + siteID + ".msg.history",
		"chat.user.*.request.room.*." + siteID + ".msg.get",
		"chat.user.*.request.room.*." + siteID + ".msg.surrounding",
		"chat.user.*.request.room.*." + siteID + ".msg.thread",
	}
	historyStubs := make([]*nats.Subscription, 0, len(wildcards))
	for _, wc := range wildcards {
		sub, err := nc.Subscribe(wc, func(m *nats.Msg) {
			_ = m.Respond([]byte(`{"messages":[]}`))
		})
		require.NoError(t, err)
		historyStubs = append(historyStubs, sub)
	}
	defer func() {
		for _, s := range historyStubs {
			_ = s.Unsubscribe()
		}
	}()

	metrics := NewMetrics()
	collector := NewCollector(metrics, preset.Name)
	requester := &natsRequester{pool: &ConnPool{observer: nc}}

	gen := NewHistoryReadGenerator(&HistoryReadConfig{
		Preset: &preset, Fixtures: fixtures, SiteID: siteID,
		Rate: 100, Requester: requester, Metrics: metrics,
		Collector:  collector,
		MessageIDs: []string{"m-stub-1", "m-stub-2"},
		Timeout:    2 * time.Second,
	}, 42)

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	require.NoError(t, gen.Run(runCtx))

	stats := collector.RequestStats()
	require.NotEmpty(t, stats, "expected per-kind request stats after end-to-end run")
	totalCount, totalErrors := 0, 0
	for _, s := range stats {
		totalCount += s.Count
		totalErrors += s.Errors
	}
	require.Greater(t, totalCount, 100, "expected ≥100 history requests; got %d", totalCount)
	require.Zero(t, totalErrors, "expected zero errors; got %d", totalErrors)
}

// TestSearchReadScenario_EndToEnd_StubService — same shape against a
// fake search-service.
func TestSearchReadScenario_EndToEnd_StubService(t *testing.T) {
	ctx := context.Background()
	natsURI, stopNATS := setupNATS(t)
	defer stopNATS()

	nc, err := nats.Connect(natsURI)
	require.NoError(t, err)
	defer nc.Drain()

	preset, _ := BuiltinPreset("search-read")
	fixtures := BuildFixtures(&preset, 42, "site-test")

	searchStubs := make([]*nats.Subscription, 0, 2)
	for _, wc := range []string{
		"chat.user.*.request.search.messages",
		"chat.user.*.request.search.rooms",
	} {
		sub, err := nc.Subscribe(wc, func(m *nats.Msg) {
			_ = m.Respond([]byte(`{"messages":[]}`))
		})
		require.NoError(t, err)
		searchStubs = append(searchStubs, sub)
	}
	defer func() {
		for _, s := range searchStubs {
			_ = s.Unsubscribe()
		}
	}()

	metrics := NewMetrics()
	collector := NewCollector(metrics, preset.Name)
	gen := NewSearchReadGenerator(&SearchReadConfig{
		Preset: &preset, Fixtures: fixtures, SiteID: "site-test",
		Rate: 100, Requester: &natsRequester{pool: &ConnPool{observer: nc}}, Metrics: metrics,
		Collector: collector, Timeout: 2 * time.Second,
	}, 42)

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	require.NoError(t, gen.Run(runCtx))

	stats := collector.RequestStats()
	require.NotEmpty(t, stats)
	totalCount, totalErrors := 0, 0
	for _, s := range stats {
		totalCount += s.Count
		totalErrors += s.Errors
	}
	require.Greater(t, totalCount, 100)
	require.Zero(t, totalErrors)
}

// TestRoomRPCScenario_EndToEnd_StubService — same shape against a
// fake room-service.
func TestRoomRPCScenario_EndToEnd_StubService(t *testing.T) {
	ctx := context.Background()
	natsURI, stopNATS := setupNATS(t)
	defer stopNATS()

	nc, err := nats.Connect(natsURI)
	require.NoError(t, err)
	defer nc.Drain()

	preset, _ := BuiltinPreset("room-rpc")
	fixtures := BuildFixtures(&preset, 42, "site-test")

	wildcards := []string{
		"chat.user.*.request.rooms.list",
		"chat.user.*.request.rooms.get.*",
		"chat.user.*.request.room.*.site-test.member.list",
		"chat.user.*.request.room.site-test.create",
		"chat.user.*.request.room.*.site-test.member.add",
	}
	roomStubs := make([]*nats.Subscription, 0, len(wildcards))
	for _, wc := range wildcards {
		sub, err := nc.Subscribe(wc, func(m *nats.Msg) {
			_ = m.Respond([]byte(`{}`))
		})
		require.NoError(t, err)
		roomStubs = append(roomStubs, sub)
	}
	defer func() {
		for _, s := range roomStubs {
			_ = s.Unsubscribe()
		}
	}()

	metrics := NewMetrics()
	collector := NewCollector(metrics, preset.Name)
	gen := NewRoomRPCGenerator(&RoomRPCConfig{
		Preset: &preset, Fixtures: fixtures, SiteID: "site-test",
		Rate: 100, Requester: &natsRequester{pool: &ConnPool{observer: nc}}, Metrics: metrics,
		Collector: collector, Timeout: 2 * time.Second,
	}, 42)

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	require.NoError(t, gen.Run(runCtx))

	stats := collector.RequestStats()
	require.NotEmpty(t, stats)
	totalCount, totalErrors := 0, 0
	for _, s := range stats {
		totalCount += s.Count
		totalErrors += s.Errors
	}
	require.Greater(t, totalCount, 100)
	require.Zero(t, totalErrors)
}
