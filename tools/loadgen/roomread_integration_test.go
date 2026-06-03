//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil"
)

// TestRoomReadWorkload_EndToEnd seeds read-state fixtures into a real Mongo,
// then drives the generator briefly against a stub message.read responder and
// asserts the seeded read-state is present and the generator records samples.
func TestRoomReadWorkload_EndToEnd(t *testing.T) {
	ctx := context.Background()

	db := testutil.MongoDB(t, "loadgen_roomread")

	p, ok := BuiltinPreset("small")
	require.True(t, ok)
	siteID := "site-test"
	now := time.Now().UTC()
	fixtures := BuildRoomReadFixtures(&p, 42, siteID, now)

	require.NoError(t, Seed(ctx, db, &fixtures))

	// Cross-check: every seeded room carries a future LastMsgAt + a floor.
	roomCount, err := db.Collection("rooms").CountDocuments(ctx,
		map[string]any{"lastMsgAt": map[string]any{"$gt": now}})
	require.NoError(t, err)
	assert.Equal(t, int64(len(fixtures.Rooms)), roomCount, "all rooms should have a future lastMsgAt")

	// Stub message.read responder.
	nc, err := nats.Connect(testutil.NATS(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = nc.Drain() })

	sub, err := nc.Subscribe(subject.MessageReadWildcard(siteID), func(m *nats.Msg) {
		_ = m.Respond([]byte(`{"status":"accepted"}`))
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Drive the generator briefly.
	collector := NewRoomReadCollector()
	requester := newNATSHistoryRequester(nc)
	gen := newRoomReadGenerator(&roomReadGeneratorConfig{
		Fixtures:       &fixtures,
		SiteID:         siteID,
		Rate:           50,
		RequestTimeout: 2 * time.Second,
		Requester:      requester,
		Collector:      collector,
		MaxInFlight:    16,
	}, 42)

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	require.NoError(t, gen.Run(runCtx))

	assert.NotEmpty(t, collector.Samples(), "generator produced zero samples")
	assert.Equal(t, 0, collector.TimeoutErrors(), "no requests should time out against the stub")
	assert.Equal(t, 0, collector.ReplyErrors(), "stub never returns an error")
	assert.Equal(t, 0, collector.BadReplyCount(), "stub always returns accepted")
}
