package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// runSeedSearchSyncACL is the seed-time helper called from dispatch.runSeed
// when --with-search-sync-acl is set. Two things must hold:
//   - it publishes one ACL OutboxMemberAdded per unique (account, room)
//   - it then waits `wait` for the ES refresh, returning early on ctx cancel
//
// The wait is integral to the helper (not the caller) so a dispatch-level
// test can assert wait semantics without spinning up a full runSeed.
func TestRunSeedSearchSyncACL_PublishesAndWaits(t *testing.T) {
	pub := &recordingPublisher{}
	subs := []model.Subscription{
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
		{RoomID: "room-2", User: model.SubscriptionUser{Account: "bob"}, RoomType: model.RoomTypeChannel},
	}
	const wait = 30 * time.Millisecond

	start := time.Now()
	pairs, err := runSeedSearchSyncACL(context.Background(), pub, "site-local", subs, wait)
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, 2, pairs)
	assert.GreaterOrEqual(t, elapsed, wait,
		"helper must wait at least the configured ACL wait so ES refresh has time to expose the user-room docs")

	calls := pub.snapshot()
	require.Len(t, calls, 2)
	wantSubj := subject.InboxMemberAdded("site-local")
	for _, c := range calls {
		assert.Equal(t, wantSubj, c.subject)
	}
}

func TestRunSeedSearchSyncACL_ZeroWaitSkipsTimer(t *testing.T) {
	// wait <= 0 must short-circuit the timer — useful in unit tests and as a
	// safety net if an operator passes --search-sync-acl-wait=0 expecting
	// "publish only, no wait".
	pub := &recordingPublisher{}
	subs := []model.Subscription{
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
	}
	start := time.Now()
	pairs, err := runSeedSearchSyncACL(context.Background(), pub, "site-local", subs, 0)
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, 1, pairs)
	assert.Less(t, elapsed, 100*time.Millisecond, "zero wait must not block")
}

func TestRunSeedSearchSyncACL_PropagatesPublishError(t *testing.T) {
	pub := &erroringPublisher{err: errors.New("nats unreachable")}
	subs := []model.Subscription{
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
	}
	_, err := runSeedSearchSyncACL(context.Background(), pub, "site-local", subs, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nats unreachable",
		"publish failure must surface so the seed subcommand exits non-zero")
}

func TestRunSeedSearchSyncACL_CtxCancelStopsWaitEarly(t *testing.T) {
	pub := &recordingPublisher{}
	subs := []model.Subscription{
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
	}
	// Use a long wait so a successful early-exit-on-cancel is unambiguous.
	const wait = 10 * time.Second
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel just after the publishes complete. Use a goroutine that waits
	// briefly for the publish loop to drain (publishes are sync so 5ms is
	// generous), then cancels.
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Give the publish a moment to drain. We deliberately avoid time.Sleep
		// as a sync primitive elsewhere, but here we're simulating an external
		// shutdown signal arriving mid-wait — that IS a time-based event.
		t := time.NewTimer(20 * time.Millisecond)
		defer t.Stop()
		<-t.C
		cancel()
	}()

	start := time.Now()
	_, err := runSeedSearchSyncACL(ctx, pub, "site-local", subs, wait)
	elapsed := time.Since(start)
	<-done

	require.NoError(t, err, "cancellation during wait is a clean exit, not an error")
	assert.Less(t, elapsed, 2*time.Second,
		"cancel must short-circuit the 10s wait; if elapsed ~ 10s the cancel arm is missing")
}

func TestRunSeedSearchSyncACL_EmptySubsIsNoOpAndSkipsWait(t *testing.T) {
	// With no subscriptions there is nothing to publish AND no reason to wait
	// for ES — the wait is purely about the publishes being visible. Empty
	// fixtures should exit immediately.
	pub := &recordingPublisher{}
	const wait = 10 * time.Second
	start := time.Now()
	pairs, err := runSeedSearchSyncACL(context.Background(), pub, "site-local", nil, wait)
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, 0, pairs)
	assert.Empty(t, pub.snapshot())
	assert.Less(t, elapsed, 100*time.Millisecond,
		"empty fixture set must skip the wait — nothing was published to wait for")
}
