//go:build integration

package main

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil"
)

// TestReadReceiptWorkload_EndToEnd seeds Mongo fixtures + reader state, stands a
// stub read-receipt responder, and drives the generator briefly. It verifies
// the seed stamped lastSeenAt and the generator round-trips successfully.
func TestReadReceiptWorkload_EndToEnd(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "loadgen_readreceipt")

	preset, ok := BuiltinHistoryPreset("history-small")
	require.True(t, ok)
	siteID := "site-test"
	const seed = int64(42)
	const readRatio = 0.7

	res := BuildHistoryFixtures(&preset, seed, siteID, time.Now().UTC())
	plan := res.FullPlan()
	require.NoError(t, Seed(ctx, db, &res.Fixtures))
	require.NoError(t, SeedReadReceiptState(ctx, db, res.Fixtures.Subscriptions, &plan, readRatio, seed))

	// Expected stamped count: ceil(readRatio*roomSize) per room that has a
	// top-level message.
	latest := latestTopLevelByRoom(&plan)
	subsByRoom := map[string]int{}
	for i := range res.Fixtures.Subscriptions {
		subsByRoom[res.Fixtures.Subscriptions[i].RoomID]++
	}
	expectedStamped := 0
	for roomID, n := range subsByRoom {
		if _, has := latest[roomID]; has {
			expectedStamped += int(math.Ceil(readRatio * float64(n)))
		}
	}
	stamped, err := db.Collection("subscriptions").CountDocuments(ctx,
		bson.M{"lastSeenAt": bson.M{"$exists": true}})
	require.NoError(t, err)
	assert.Equal(t, int64(expectedStamped), stamped, "stamped lastSeenAt count")

	// Stub responder mirroring room-service's read-receipt subject layout.
	nc, err := nats.Connect(testutil.NATS(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = nc.Drain() })

	sub, err := nc.Subscribe(subject.MessageReadReceiptWildcard(siteID), func(m *nats.Msg) {
		_ = m.Respond([]byte(`{"readers":[]}`))
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Drive the generator briefly.
	collector := NewReadReceiptCollector()
	gen := NewReadReceiptGenerator(&ReadReceiptGeneratorConfig{
		Targets:        deriveReadReceiptTargets(&plan),
		SiteID:         siteID,
		Rate:           50,
		RequestTimeout: 2 * time.Second,
		Requester:      newNATSReadReceiptRequester(nc),
		Collector:      collector,
		MaxInFlight:    16,
	}, seed)

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	require.NoError(t, gen.Run(runCtx))
	time.Sleep(500 * time.Millisecond) // drain trailing replies

	assert.NotEmpty(t, collector.Samples(), "generator produced zero samples")
	assert.Equal(t, 0, collector.Failed(), "stub responder should yield zero failures")
}
