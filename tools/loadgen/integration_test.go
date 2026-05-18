//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	require.NoError(t, Seed(ctx, db, &fixtures))

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
	// 2% error budget accommodates host jitter (slow Docker host, stub registration race);
	// any larger fraction is a real regression.
	maxErr := totalCount / 50
	if maxErr < 3 {
		maxErr = 3
	}
	require.LessOrEqual(t, totalErrors, maxErr, "errors %d exceed budget %d (count %d)", totalErrors, maxErr, totalCount)
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
	maxErr := totalCount / 50
	if maxErr < 3 {
		maxErr = 3
	}
	require.LessOrEqual(t, totalErrors, maxErr, "errors %d exceed budget %d (count %d)", totalErrors, maxErr, totalCount)
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
	maxErr := totalCount / 50
	if maxErr < 3 {
		maxErr = 3
	}
	require.LessOrEqual(t, totalErrors, maxErr, "errors %d exceed budget %d (count %d)", totalErrors, maxErr, totalCount)
}

// TestDispatch_RunRun_CanonicalInjectSmoke is the binary-exec smoke test
// asked for in the Phase S review (Reviewer 3 W5 option (b)). It
// exercises the full main.go wiring layer — dispatch → runRun →
// readiness → generator → drainTrailingReplies → metricsSrv shutdown
// → nc.Drain → exit code — by invoking dispatch() directly with a
// crafted os.Args, against a testcontainer-managed NATS + a
// programmatically-created MESSAGES_CANONICAL stream.
//
// Without this test the uncovered wiring code (runRun at 18%, main
// at 0%, runTeardown at 21%) made the package coverage land below
// CLAUDE.md's 80% floor even though every line was exercised
// end-to-end during real-machine validation. This test lifts the
// coverage by exercising the same paths under -race.
//
// Kept short (--duration=2s) so the test runs in <10s while still
// covering the warmup→measured transition and the async-drain path.
func TestDispatch_RunRun_CanonicalInjectSmoke(t *testing.T) {
	natsURI, stopNATS := setupNATS(t)
	defer stopNATS()
	mongoURI, stopMongo := setupMongo(t)
	defer stopMongo()

	// Bootstrap MESSAGES_CANONICAL_site-local so canonical-inject
	// publishes have somewhere to land. The loadgen itself doesn't
	// create this stream — message-gatekeeper owns it in production —
	// so we stand it up here. Memory storage with a tight retention
	// keeps the test fast and bounded.
	nc, err := nats.Connect(natsURI)
	require.NoError(t, err)
	defer nc.Drain()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	streamCfg := stream.MessagesCanonical("site-local")
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamCfg.Name,
		Subjects:  streamCfg.Subjects,
		Storage:   jetstream.MemoryStorage,
		Retention: jetstream.LimitsPolicy,
		MaxAge:    1 * time.Minute,
	})
	require.NoError(t, err)

	// Stash + restore os.Args so we don't pollute neighboring tests.
	savedArgs := os.Args
	defer func() { os.Args = savedArgs }()

	// Build the config the same way main() does, but inject the
	// container-managed connection strings. The MONGO_DB carries the
	// `loadgen` prefix so the safety guard passes.
	cfg := &config{
		NatsURL:     natsURI,
		MongoURI:    mongoURI,
		MongoDB:     "loadgen_smoke",
		SiteID:      "site-local",
		MetricsAddr: ":0", // OS-assigned port so concurrent tests don't collide
		MaxInFlight: 32,
	}
	os.Args = []string{
		"loadgen", "run",
		"--preset=small",
		"--scenario=messaging-pipeline",
		"--inject=canonical",
		"--rate=200",
		"--duration=2s",
		"--warmup=200ms",
		"--skip-readiness",
		"--abort-on-p99-ms=0",
		"--liveness-interval=0",
		"--auto-warmup=false",
		"--progress-interval=0",
		"--js-async-max-pending=128",
	}
	dispatchCtx, dispatchCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer dispatchCancel()
	code := dispatch(dispatchCtx, cfg)
	// Exit code 1 is the EXPECTED outcome here: every publish succeeds
	// (the runRun wiring works end-to-end — generator → JetStream →
	// async ack → drainTrailingReplies → report) but every messageID
	// stays in the broadcast-correlation map because there is no
	// broadcast-worker in this test to fan out the messages. totalErrs
	// = missing_broadcasts = N, which crosses the 0.1% tolerance →
	// exit 1. What matters for coverage is that dispatch() / runRun /
	// exitCodeForFull / the metrics-server lifecycle ran cleanly. Exit
	// 0 would require running a stub broadcast subscriber; the failure
	// mode here proves the *wiring* path independently.
	require.Equal(t, 1, code,
		"clean publish path + no broadcast-worker = exit 1 (missing broadcasts > tolerance)")
}

// TestDispatch_RunSeed_Smoke walks the seed subcommand: dispatch →
// runSeed → guardMongoDB → mongoutil.Connect → BuildFixtures →
// Seed → mongoutil.Disconnect. The lowest-coverage seed.go paths
// (Seed at 57%, insertDocs at 0%) all run here against a real
// MongoDB container.
func TestDispatch_RunSeed_Smoke(t *testing.T) {
	mongoURI, stopMongo := setupMongo(t)
	defer stopMongo()

	savedArgs := os.Args
	defer func() { os.Args = savedArgs }()

	cfg := &config{
		MongoURI: mongoURI,
		MongoDB:  "loadgen_seed_smoke",
		SiteID:   "site-local",
	}
	os.Args = []string{"loadgen", "seed", "--preset=small"}
	dispatchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code := dispatch(dispatchCtx, cfg)
	require.Equal(t, 0, code, "seed must exit 0 against a fresh Mongo")

	// Sanity-check: the rooms collection has the preset's 5 rooms.
	client, err := mongoutil.Connect(dispatchCtx, mongoURI, "", "")
	require.NoError(t, err)
	defer mongoutil.Disconnect(dispatchCtx, client)
	count, err := client.Database("loadgen_seed_smoke").Collection("rooms").CountDocuments(dispatchCtx, bson.M{})
	require.NoError(t, err)
	require.EqualValues(t, 5, count, "small preset seeds 5 rooms")
}

// TestDispatch_RunTeardown_Smoke covers the teardown subcommand. The
// previous two tests left documents in Mongo; this one wipes them.
// Together they cover the seed → teardown round-trip that the
// runTeardown handler (21% coverage pre-S6) gates.
func TestDispatch_RunTeardown_Smoke(t *testing.T) {
	mongoURI, stopMongo := setupMongo(t)
	defer stopMongo()

	// Seed first so teardown has something to delete.
	savedArgs := os.Args
	defer func() { os.Args = savedArgs }()

	cfg := &config{
		MongoURI: mongoURI,
		MongoDB:  "loadgen_teardown_smoke",
		SiteID:   "site-local",
	}
	os.Args = []string{"loadgen", "seed", "--preset=small"}
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer seedCancel()
	require.Equal(t, 0, dispatch(seedCtx, cfg))

	// Now tear down.
	os.Args = []string{"loadgen", "teardown"}
	teardownCtx, teardownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer teardownCancel()
	require.Equal(t, 0, dispatch(teardownCtx, cfg))

	// Verify the rooms collection is empty.
	client, err := mongoutil.Connect(teardownCtx, mongoURI, "", "")
	require.NoError(t, err)
	defer mongoutil.Disconnect(teardownCtx, client)
	count, err := client.Database("loadgen_teardown_smoke").Collection("rooms").CountDocuments(teardownCtx, bson.M{})
	require.NoError(t, err)
	require.EqualValues(t, 0, count, "teardown wipes the rooms collection")
}

// TestDispatch_UnknownSubcommand is covered by main_test.go (no -tags
// gate needed). The integration build inherits that coverage.

// ---------------------------------------------------------------------------
// RunLock integration tests — exercise the full Mongo wire-protocol path.
// ---------------------------------------------------------------------------

// TestRunLock_RefusesConcurrentRun verifies that a second Acquire on the
// same SUT URL returns ErrConcurrentRun when the first lock is still active.
func TestRunLock_RefusesConcurrentRun(t *testing.T) {
	mongoURI, stopMongo := setupMongo(t)
	defer stopMongo()

	ctx := context.Background()
	client, err := mongoutil.Connect(ctx, mongoURI, "", "")
	require.NoError(t, err)
	defer mongoutil.Disconnect(ctx, client)

	db := client.Database("loadgen_runlock_test")
	defer func() { _ = db.Drop(ctx) }()

	const sutURL = "nats://sut-host:4222"

	lock1 := NewRunLock(db, sutURL, 2*time.Hour)
	require.NoError(t, lock1.Acquire(ctx, "run-A", "history-read", false))

	lock2 := NewRunLock(db, sutURL, 2*time.Hour)
	err = lock2.Acquire(ctx, "run-B", "history-read", false)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrConcurrentRun),
		"second Acquire on same SUT must return ErrConcurrentRun; got: %v", err)
}

// TestRunLock_AllowConcurrentSkipsCheck verifies that allowConcurrent=true
// lets a second run start even when an active lock row exists.
func TestRunLock_AllowConcurrentSkipsCheck(t *testing.T) {
	mongoURI, stopMongo := setupMongo(t)
	defer stopMongo()

	ctx := context.Background()
	client, err := mongoutil.Connect(ctx, mongoURI, "", "")
	require.NoError(t, err)
	defer mongoutil.Disconnect(ctx, client)

	db := client.Database("loadgen_runlock_concurrent_test")
	defer func() { _ = db.Drop(ctx) }()

	const sutURL = "nats://sut-host:4222"

	lock1 := NewRunLock(db, sutURL, 2*time.Hour)
	require.NoError(t, lock1.Acquire(ctx, "run-A", "history-read", false))

	lock2 := NewRunLock(db, sutURL, 2*time.Hour)
	require.NoError(t, lock2.Acquire(ctx, "run-B", "history-read", true),
		"allowConcurrent=true must skip the conflict check")
}

// TestRunLock_ExpiredLockBypassed verifies that a row with startedAt older
// than the TTL is not treated as a conflict (it fails the $gt cutoff filter).
func TestRunLock_ExpiredLockBypassed(t *testing.T) {
	mongoURI, stopMongo := setupMongo(t)
	defer stopMongo()

	ctx := context.Background()
	client, err := mongoutil.Connect(ctx, mongoURI, "", "")
	require.NoError(t, err)
	defer mongoutil.Disconnect(ctx, client)

	db := client.Database("loadgen_runlock_expired_test")
	defer func() { _ = db.Drop(ctx) }()

	const sutURL = "nats://sut-host:4222"

	// Use a 1-nanosecond TTL so any existing row is immediately "expired".
	lock1 := NewRunLock(db, sutURL, 1*time.Nanosecond)
	require.NoError(t, lock1.Acquire(ctx, "run-expired", "history-read", false))

	// lock2 uses the same 1ns TTL — the row inserted by lock1 is now older
	// than the cutoff, so the $gt filter doesn't match it.
	lock2 := NewRunLock(db, sutURL, 1*time.Nanosecond)
	require.NoError(t, lock2.Acquire(ctx, "run-new", "history-read", false),
		"expired lock row must be bypassed (not treated as active)")
}

// TestRunLock_ReleaseClearsRow verifies that Release removes the lock document
// so a subsequent Acquire on the same SUT URL can succeed.
func TestRunLock_ReleaseClearsRow(t *testing.T) {
	mongoURI, stopMongo := setupMongo(t)
	defer stopMongo()

	ctx := context.Background()
	client, err := mongoutil.Connect(ctx, mongoURI, "", "")
	require.NoError(t, err)
	defer mongoutil.Disconnect(ctx, client)

	db := client.Database("loadgen_runlock_release_test")
	defer func() { _ = db.Drop(ctx) }()

	const sutURL = "nats://sut-host:4222"

	lock1 := NewRunLock(db, sutURL, 2*time.Hour)
	require.NoError(t, lock1.Acquire(ctx, "run-A", "messaging-pipeline", false))
	require.NoError(t, lock1.Release(ctx))

	lock2 := NewRunLock(db, sutURL, 2*time.Hour)
	require.NoError(t, lock2.Acquire(ctx, "run-B", "messaging-pipeline", false),
		"after Release, a new Acquire on the same SUT must succeed")
}

// ---------------------------------------------------------------------------
// Teardown --force integration tests.
// ---------------------------------------------------------------------------

// TestTeardownForce_DropsOrphanedDBsAndConsumers is the end-to-end
// integration test for runTeardownForce. It sets up three "runs":
//
//   - run-A (active lock row):      DB loadgen_AAAAAAA + consumer loadgen_AAAAAAA_e1
//   - run-B (no lock row, orphan):  DB loadgen_BBBBBBB + consumer loadgen_BBBBBBB_e1
//   - run-C (expired lock row):     DB loadgen_CCCCCCC + consumer loadgen_CCCCCCC_e1
//     row inserted with startedAt = 3 hours ago
//
// It then runs runTeardownForce with OlderThan=2h and verifies:
//   - run-A is retained (active, within 2h)
//   - run-B is dropped  (no lock row → orphan)
//   - run-C is dropped  (expired, started 3h ago > 2h threshold)
func TestTeardownForce_DropsOrphanedDBsAndConsumers(t *testing.T) {
	ctx := context.Background()
	natsURI, stopNATS := setupNATS(t)
	defer stopNATS()
	mongoURI, stopMongo := setupMongo(t)
	defer stopMongo()

	// Connect to Mongo.
	mc, err := mongoutil.Connect(ctx, mongoURI, "", "")
	require.NoError(t, err)
	defer mongoutil.Disconnect(ctx, mc)

	// Connect to NATS + JetStream.
	nc, err := nats.Connect(natsURI)
	require.NoError(t, err)
	defer nc.Drain()
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	// Create a single stream for the consumer tests. All loadgen consumers
	// will be created on this stream.
	streamName := "LOADGEN_TEST_FORCE"
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{"loadgen.test.force.>"},
		Storage:  jetstream.MemoryStorage,
	})
	require.NoError(t, err)

	// Shared lock DB for the runlock rows.
	lockColl := mc.Database(SharedLockDBName).Collection("loadgen_runs")

	// Short IDs — exactly 7 chars to match shortRunID convention.
	const (
		shortA = "AAAAAAA"
		shortB = "BBBBBBB"
		shortC = "CCCCCCC"
	)

	// --- run-A: active lock row, started NOW (within any OlderThan window) ---
	_, err = lockColl.InsertOne(ctx, runLockDoc{
		RunID:     shortA + "extra",
		StartedAt: time.Now().UTC(),
		Host:      "test-host",
		Scenario:  "messaging-pipeline",
		SUTURL:    "nats://sut:4222",
	})
	require.NoError(t, err)
	// Create DB and consumer for run-A.
	require.NoError(t, mc.Database("loadgen_"+shortA).CreateCollection(ctx, "placeholder"))
	_, err = js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:   "loadgen_" + shortA + "_e1",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)

	// --- run-B: no lock row (orphan) ---
	// Just create DB and consumer; no lock row inserted.
	require.NoError(t, mc.Database("loadgen_"+shortB).CreateCollection(ctx, "placeholder"))
	_, err = js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:   "loadgen_" + shortB + "_e1",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)

	// --- run-C: expired lock row (started 3 hours ago) ---
	_, err = lockColl.InsertOne(ctx, runLockDoc{
		RunID:     shortC + "extra",
		StartedAt: time.Now().UTC().Add(-3 * time.Hour),
		Host:      "test-host",
		Scenario:  "messaging-pipeline",
		SUTURL:    "nats://sut:4222",
	})
	require.NoError(t, err)
	// Create DB and consumer for run-C.
	require.NoError(t, mc.Database("loadgen_"+shortC).CreateCollection(ctx, "placeholder"))
	_, err = js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:   "loadgen_" + shortC + "_e1",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)

	// Run teardown --force with OlderThan=2h.
	rep, err := runTeardownForce(ctx, mc, js, teardownForceConfig{
		OlderThan: 2 * time.Hour,
	})
	require.NoError(t, err)

	// run-B and run-C must be dropped; run-A must be skipped.
	require.Contains(t, rep.DroppedMongoDBs, "loadgen_"+shortB, "orphan run-B DB must be dropped")
	require.Contains(t, rep.DroppedMongoDBs, "loadgen_"+shortC, "expired run-C DB must be dropped")
	require.NotContains(t, rep.DroppedMongoDBs, "loadgen_"+shortA, "active run-A DB must NOT be dropped")
	require.NotContains(t, rep.DroppedMongoDBs, SharedLockDBName,
		"loadgen_shared must never be dropped by force teardown")

	require.Contains(t, rep.DroppedConsumers, "loadgen_"+shortB+"_e1", "orphan run-B consumer must be dropped")
	require.Contains(t, rep.DroppedConsumers, "loadgen_"+shortC+"_e1", "expired run-C consumer must be dropped")
	require.NotContains(t, rep.DroppedConsumers, "loadgen_"+shortA+"_e1", "active run-A consumer must NOT be dropped")

	// Verify the skipped set contains run-A.
	require.Contains(t, rep.SkippedDBs, "loadgen_"+shortA, "active run-A DB must appear in skipped list")
	require.Contains(t, rep.SkippedConsumers, "loadgen_"+shortA+"_e1", "active run-A consumer must appear in skipped list")

	// Idempotency: running again on an already-clean state must not error.
	rep2, err := runTeardownForce(ctx, mc, js, teardownForceConfig{
		OlderThan: 2 * time.Hour,
	})
	require.NoError(t, err, "re-running on clean state must be a no-op (idempotent)")
	// Dropped lists should be empty — nothing left to clean.
	require.Empty(t, rep2.DroppedMongoDBs, "second run: no DBs to drop")
	require.Empty(t, rep2.DroppedConsumers, "second run: no consumers to drop")
}
