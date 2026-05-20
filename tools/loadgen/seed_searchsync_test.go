package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// SeedSearchSyncACL is the seed-time entry point that publishes one synthetic
// OutboxMemberAdded event per unique (account, roomID) tuple. It is the
// load-bearing helper for `loadgen seed --with-search-sync-acl` and must
// produce wire output that is byte-identical (modulo the time-now Timestamp)
// to the legacy Run-time bootstrapSearchSyncACL — search-sync-worker doesn't
// care which subcommand sent the event.
func TestSeedSearchSyncACL_OneEventPerUniquePair(t *testing.T) {
	pub := &recordingPublisher{}
	subs := []model.Subscription{
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel}, // dup
		{RoomID: "room-2", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "bob"}, RoomType: model.RoomTypeChannel},
	}
	pairs, err := SeedSearchSyncACL(context.Background(), pub, "site-local", subs)
	require.NoError(t, err)
	assert.Equal(t, 3, pairs, "unique (account, room) pair count must drive the publish count")

	calls := pub.snapshot()
	assert.Len(t, calls, 3, "duplicate (account, room) pair must collapse to a single ACL publish")
	wantSubj := subject.InboxMemberAdded("site-local")
	for _, c := range calls {
		assert.Equal(t, wantSubj, c.subject,
			"seed-time ACL events must hit the same subject as the Run-time bootstrap (search-sync-worker user-room-sync consumer)")
		var evt model.OutboxEvent
		require.NoError(t, json.Unmarshal(c.data, &evt))
		assert.Equal(t, model.OutboxMemberAdded, evt.Type)
		assert.Equal(t, "site-local", evt.SiteID)
		assert.Greater(t, evt.Timestamp, int64(0), "event timestamp gates the painless LWW logic; must be > 0")

		var inner model.InboxMemberEvent
		require.NoError(t, json.Unmarshal(evt.Payload, &inner))
		assert.NotEmpty(t, inner.RoomID)
		assert.Len(t, inner.Accounts, 1)
		assert.Nil(t, inner.HistorySharedSince,
			"unrestricted ACL: nil HSS pointer is the painless contract for `hss <= 0 → unrestricted`")
	}
}

func TestSeedSearchSyncACL_PropagatesPublishError(t *testing.T) {
	pub := &erroringPublisher{err: errors.New("nats down")}
	subs := []model.Subscription{
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
	}
	_, err := SeedSearchSyncACL(context.Background(), pub, "site-local", subs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nats down",
		"seed-time helper must surface the underlying publish error so dispatch can exit non-zero")
}

func TestSeedSearchSyncACL_HonorsCtxCancellation(t *testing.T) {
	pub := &recordingPublisher{}
	subs := []model.Subscription{
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
		{RoomID: "room-2", User: model.SubscriptionUser{Account: "bob"}, RoomType: model.RoomTypeChannel},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := SeedSearchSyncACL(ctx, pub, "site-local", subs)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled,
		"pre-cancelled ctx must surface as context.Canceled, not be silently absorbed")
}

func TestSeedSearchSyncACL_EmptySubsIsNoOp(t *testing.T) {
	pub := &recordingPublisher{}
	pairs, err := SeedSearchSyncACL(context.Background(), pub, "site-local", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, pairs)
	assert.Empty(t, pub.snapshot(),
		"empty fixture set must publish nothing — no NATS round-trips on a Mongo-only seed")
}

// SeedSearchSyncACL is the public name; bootstrapSearchSyncACL is the legacy
// internal helper used at Run start. Both produce the same wire format so an
// operator who seeds via the new flag and then runs WITHOUT
// --search-sync-skip-acl-bootstrap will see the Run-time bootstrap publish
// identical OutboxEvents (painless LWW handles the redundant write as a
// no-op). Pin that contract.
func TestSeedSearchSyncACL_WireOutputMatchesRunTimeBootstrap(t *testing.T) {
	subs := []model.Subscription{
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
		{RoomID: "room-2", User: model.SubscriptionUser{Account: "bob"}, RoomType: model.RoomTypeChannel},
	}

	seedPub := &recordingPublisher{}
	_, err := SeedSearchSyncACL(context.Background(), seedPub, "site-local", subs)
	require.NoError(t, err)

	runPub := &recordingPublisher{}
	_, err = bootstrapSearchSyncACL(context.Background(), runPub, "site-local", subs)
	require.NoError(t, err)

	seedCalls := seedPub.snapshot()
	runCalls := runPub.snapshot()
	require.Equal(t, len(seedCalls), len(runCalls), "call count must match")

	for i := range seedCalls {
		assert.Equal(t, runCalls[i].subject, seedCalls[i].subject,
			"call %d subject mismatch: seed and Run bootstrap MUST target the same INBOX subject so search-sync-worker treats both writes identically", i)

		// Wire payloads can differ only on the Timestamp field (different clock
		// reads). Strip it and compare the remaining envelope + payload bytes.
		seedEvt := unmarshalOutboxForCompare(t, seedCalls[i].data)
		runEvt := unmarshalOutboxForCompare(t, runCalls[i].data)
		assert.Equal(t, runEvt, seedEvt,
			"OutboxEvent envelope and InboxMemberEvent payload (ex-timestamps) must match; downstream painless treats divergence as a separate write")
	}
}

// unmarshalOutboxForCompare decodes an OutboxEvent and its inner
// InboxMemberEvent, zeroing both timestamps so two calls separated by a clock
// tick are still byte-comparable.
func unmarshalOutboxForCompare(t *testing.T, data []byte) outboxForCompare {
	t.Helper()
	var evt model.OutboxEvent
	require.NoError(t, json.Unmarshal(data, &evt))
	var inner model.InboxMemberEvent
	require.NoError(t, json.Unmarshal(evt.Payload, &inner))
	evt.Timestamp = 0
	inner.Timestamp = 0
	inner.JoinedAt = 0
	return outboxForCompare{
		Type:       evt.Type,
		SiteID:     evt.SiteID,
		DestSiteID: evt.DestSiteID,
		Inner:      inner,
	}
}

type outboxForCompare struct {
	Type       model.OutboxEventType
	SiteID     string
	DestSiteID string
	Inner      model.InboxMemberEvent
}
