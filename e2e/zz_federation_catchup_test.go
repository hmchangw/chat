//go:build e2e

package e2e

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/e2e/harness"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// Stops inbox-worker-b, publishes 20 cross-site invites, restarts, asserts
// the backlog drains into mongo-b. File is zz_* so it runs last among
// federation tests; the actual fix lives in inbox-worker/bootstrap.go.
func TestFederation_CatchUpAfterOutage(t *testing.T) {
	// Worker-restart consumer-reattach is racy on a shared stack; under
	// `make e2e` (testcontainers ownership) it runs reliably.
	skipUnderReuse(t, "catchup: worker-restart consumer-reattach is racy on a shared stack")

	ctx := t.Context()
	stack := stack
	harness.CaptureLogs(t, stack,
		"room-worker-a", "inbox-worker-b", "broadcast-worker-b")

	alice := stack.SiteA.Authenticate(t, ctx, "alice")
	bobOnB := stack.SiteB.Authenticate(t, ctx, "bob")
	stack.SiteA.SeedRemoteUser(t, ctx, bobOnB.Account, stack.SiteB.SiteID)

	// Stop inbox-worker-b: events accumulate on INBOX_siteB (gateway
	// sourcing keeps running) but nothing processes them.
	stopper := newWorkerLifecycle(t, ctx, "inbox-worker-b")
	stopper.Stop(t, ctx)
	t.Cleanup(func() {
		startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		stopper.Start(t, startCtx)
	})

	// Snapshot pre-count before creating rooms; gateway sourcing races us.
	jsB := stack.SiteB.JetStream(t)
	prePopulate, err := jsB.Stream(ctx, "INBOX_siteB")
	require.NoError(t, err)
	preCount := prePopulate.CachedInfo().State.Msgs

	// Create-and-invite emits one member_added per round (cross-site to bob).
	const inviteRounds = 20
	roomIDs := make([]string, 0, inviteRounds)
	for i := 0; i < inviteRounds; i++ {
		req := model.CreateRoomRequest{
			Name:  "e2e-" + t.Name() + "-r" + strconv.Itoa(i),
			Users: []string{bobOnB.Account},
		}
		var reply model.CreateRoomReply
		require.NoError(t, requestReply(
			alice.Conn(),
			subject.RoomCreate(alice.Account, stack.SiteA.SiteID),
			req, 5*time.Second, &reply,
		))
		roomIDs = append(roomIDs, reply.RoomID)
		registerRoomCleanup(t, []SiteDB{asSiteDB(t, stack.SiteA), asSiteDB(t, stack.SiteB)}, reply.RoomID)
	}
	t.Logf("created %d cross-site rooms; events queued on INBOX_siteB while worker is down", inviteRounds)

	// Wait for all 20 events on INBOX_siteB before restart (in-flight ones
	// would be lost when bootstrap briefly clears Sources).
	require.Eventually(t, func() bool {
		s, err := jsB.Stream(ctx, "INBOX_siteB")
		if err != nil {
			return false
		}
		// Each create-room invite emits one member_added event for bob
		// (the cross-site invitee). Wait for the count delta to match.
		return s.CachedInfo().State.Msgs >= preCount+uint64(inviteRounds)
	}, longPoll, 250*time.Millisecond,
		"INBOX_siteB never accumulated %d new events (gateway sourcing slow or broken); pre=%d",
		inviteRounds, preCount)
	postInfo, err := jsB.Stream(ctx, "INBOX_siteB")
	require.NoError(t, err)
	t.Logf("INBOX_siteB messages before restart: %d (delta=%d)",
		postInfo.CachedInfo().State.Msgs, postInfo.CachedInfo().State.Msgs-preCount)

	// 4. Restart inbox-worker-b.
	stopper.Start(t, ctx)

	// inbox-worker-b's BOOTSTRAP_STREAMS=true overwrites federation Sources
	// on restart; re-apply them or all later federation tests break.
	require.NoError(t, harness.BootstrapFederation(ctx, stack),
		"re-apply federation Sources after inbox-worker-b restart "+
			"(its BOOTSTRAP_STREAMS=true Update overwrites them)")

	// 5. Wait for the inbox-worker durable to drain the backlog.
	require.Eventually(t, func() bool {
		s, err := jsB.Stream(ctx, "INBOX_siteB")
		if err != nil {
			return false
		}
		c, err := s.Consumer(ctx, "inbox-worker")
		if err != nil {
			return false
		}
		info, err := c.Info(ctx)
		if err != nil {
			return false
		}
		return info.NumPending == 0
	}, longPoll, 250*time.Millisecond,
		"inbox-worker did not drain INBOX_siteB within 30s")

	// 6. All bob+roomID subscriptions must now exist on mongo-b.
	subs := stack.SiteB.MongoDB(t).Collection("subscriptions")
	for _, rid := range roomIDs {
		count, err := subs.CountDocuments(ctx, bson.M{
			"u.account": bobOnB.Account,
			"roomId":    rid,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), count,
			"missing subscription for room %s after catch-up", rid)
	}

	// Canary: prove inbox-worker-b processes fresh events end-to-end,
	// not just its backlog (otherwise the next test inherits a flaky worker).
	canaryReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name() + "-canary",
		Users: []string{bobOnB.Account},
	}
	var canaryReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, stack.SiteA.SiteID),
		canaryReq, 5*time.Second, &canaryReply,
	))
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, stack.SiteA), asSiteDB(t, stack.SiteB)}, canaryReply.RoomID)
	require.Eventually(t, func() bool {
		count, err := subs.CountDocuments(ctx, bson.M{
			"u.account": bobOnB.Account,
			"roomId":    canaryReply.RoomID,
		})
		return err == nil && count == 1
	}, 15*time.Second, 200*time.Millisecond,
		"canary cross-site invite (room %s) did not materialize on mongo-b within 15s "+
			"after inbox-worker-b restart -- worker is not steady-state, "+
			"subsequent federation tests will likely flake", canaryReply.RoomID)
}
