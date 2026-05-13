//go:build e2e

// Burst tests catch gatekeeper drops, canonical re-ordering, Cassandra
// write loss, and request-ID propagation breaks under concurrency.

package e2e

import (
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// Fire 100 distinct messages, then assert 100 Cassandra rows + 100 broadcasts.
func TestStress_BurstHundredMessages(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, site)}, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), bob.Account, roomID)

	// Bob subscribes to room broadcasts so we can count delivery.
	bobSub, err := bob.Conn().SubscribeSync(subject.RoomEvent(roomID))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bobSub.Unsubscribe() })

	// Wait for the worker durable to exist before firing the burst.
	js := site.JetStream(t)
	awaitDurableReady(t, ctx, js, stream.MessagesCanonical(site.SiteID).Name, "message-worker")

	const N = 100
	msgIDs := make([]string, N)
	for i := range msgIDs {
		msgIDs[i] = idgen.GenerateMessageID()
	}

	// Bounded fan-out (8 in flight): exposes cross-message state bugs
	// without depending on pull-iterator batch sizing.
	const concurrency = 8
	var (
		wg         sync.WaitGroup
		sendFails  atomic.Int64
		gatekeeper = make(chan struct{}, concurrency)
	)
	startSend := time.Now()
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			gatekeeper <- struct{}{}
			defer func() { <-gatekeeper }()
			reqID := idgen.GenerateRequestID()
			if err := sendAndAwaitReply(
				t,
				alice.Conn(),
				alice.Account,
				reqID,
				subject.MsgSend(alice.Account, roomID, site.SiteID),
				model.SendMessageRequest{
					ID:        msgIDs[idx],
					Content:   "burst " + msgIDs[idx],
					RequestID: reqID,
				},
				15*time.Second,
			); err != nil {
				sendFails.Add(1)
				t.Logf("send %d failed: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()
	t.Logf("100 sends in %s; failures=%d", time.Since(startSend), sendFails.Load())
	require.Zero(t, sendFails.Load(), "all 100 gatekeeper-acks must succeed")

	// Wait by msgID (seq-based wait would race sibling tests on the shared
	// durable); the last msgID's arrival implies all prior in-order acks.
	awaitMessageOnSite(t, ctx, site, msgIDs[N-1])

	// Drain bob's broadcasts; system events interleave with user messages.
	seen := make(map[string]bool, N)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(seen) < N {
		raw, mErr := bobSub.NextMsg(5 * time.Second)
		if mErr != nil {
			break
		}
		var ev model.RoomEvent
		if jerr := json.Unmarshal(raw.Data, &ev); jerr != nil {
			continue
		}
		if ev.Message == nil {
			continue
		}
		for _, id := range msgIDs {
			if ev.Message.ID == id {
				seen[id] = true
				break
			}
		}
	}
	missing := []string{}
	for _, id := range msgIDs {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "broadcasts must reach bob for every msgID; missing=%v", missing)

	// messages_by_id (PK = message_id) is the dedup table; per-msgID
	// lookup is tight, unlike messages_by_room which system events pad.
	sess := site.CassandraSession(t)
	defer sess.Close()
	for _, id := range msgIDs {
		var c int
		require.NoError(t, sess.Query(
			`SELECT COUNT(*) FROM chat.messages_by_id WHERE message_id = ?`,
			id,
		).WithContext(ctx).Scan(&c))
		require.Equal(t, 1, c,
			"messages_by_id must have exactly 1 row for msgID=%s; got %d", id, c)
	}
}
