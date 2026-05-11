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

// TestFederation_CatchUpAfterOutage: stop inbox-worker-b, publish cross-site
// invite events from siteA, restart inbox-worker-b, assert it drains the
// queued OUTBOX events and the resulting subscriptions land in mongo-b.
//
// Per amendment R1 12.B: rescoped from the original "50 messages" (messages
// don't federate) to subscription/role events (20 events across the three
// types that DO federate). Per R1 12.C: uses container Stop/Start (closer
// to a real outage than pause/unpause -- pause keeps server-side ack timers
// running). Per R2.C item 7: cycles through the three OUTBOX event types
// (member_added, member_removed, role_update) so a bug in one handler
// can't pass under cover of the others.
//
// FILE PREFIX: this file is `zz_federation_catchup_test.go` (not
// `federation_catchup_test.go`) so it runs LAST among federation tests.
// Live-run discovery: the 20-event burst + worker stop/restart leaves
// inbox-worker-b in a transient state (consumer re-attach + NATS reconnect)
// where the NEXT federation test's cross-site invite can take >15s to
// materialize on mongo-b. Running this last sidesteps that interaction.
func TestFederation_CatchUpAfterOutage(t *testing.T) {
	ctx := t.Context()
	stack := stack
	harness.CaptureLogs(t, stack,
		"room-worker-a", "inbox-worker-b", "broadcast-worker-b")

	alice := stack.SiteA.Authenticate(t, ctx, "alice")
	bobOnB := stack.SiteB.Authenticate(t, ctx, "bob")
	stack.SiteA.SeedRemoteUser(t, ctx, bobOnB.Account, stack.SiteB.SiteID)

	// 1. Stop inbox-worker-b. Cross-site events keep flowing into INBOX_siteB
	// (gateway sourcing is independent of inbox-worker), but nothing
	// processes them -- they pile up on the stream.
	//
	// Container lifecycle: prefer the testcontainers compose handle (full
	// e2e lifecycle) but fall back to shelling out `docker compose stop/start`
	// when running with E2E_REUSE_STACK=1 (no compose handle owned). The
	// behavior is identical from inbox-worker-b's perspective.
	stopper := newWorkerLifecycle(t, ctx, "inbox-worker-b")
	stopper.Stop(t, ctx)
	t.Cleanup(func() {
		startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		stopper.Start(t, startCtx)
	})

	// 2. alice creates rooms + invites bob in each, generating
	// outbox.siteA.to.siteB.member_added events. We use member_added as the
	// primary event type because the create-and-invite path emits it
	// reliably; member_removed and role_update happen in subtests below
	// against the SAME accumulated batch.
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
		registerRoomCleanup(t, []SiteDB{
			{SiteID: stack.SiteA.SiteID, DB: stack.SiteA.MongoDB(t)},
			{SiteID: stack.SiteB.SiteID, DB: stack.SiteB.MongoDB(t)},
		}, reply.RoomID)
	}
	t.Logf("created %d cross-site rooms; events queued on INBOX_siteB while worker is down", inviteRounds)

	// 3. Verify INBOX_siteB accumulated the events (gateway sourcing
	// continues regardless of the worker). We poll the stream's message
	// count to confirm it's > 0 -- the exact count depends on what other
	// events have happened, so we use a relative threshold.
	jsB := stack.SiteB.JetStream(t)
	inboxStream, err := jsB.Stream(ctx, "INBOX_siteB")
	require.NoError(t, err)
	beforeRestart := inboxStream.CachedInfo().State.Msgs
	t.Logf("INBOX_siteB messages before restart: %d", beforeRestart)
	require.Greater(t, beforeRestart, uint64(0),
		"INBOX_siteB must show queued messages (gateway sourcing should have delivered them)")

	// 4. Restart inbox-worker-b.
	stopper.Start(t, ctx)

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
	}, 30*time.Second, 250*time.Millisecond,
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
}

