//go:build integration

package mongorepo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestThreadSubscriptionRepo_ListUserThreadSubscriptions(t *testing.T) {
	db := setupMongo(t)
	repo := NewThreadSubscriptionRepo(db)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Three threads alice follows, plus one for bob and one orphan (no thread_room).
	insertThreadRoom(t, db, model.ThreadRoom{ID: "tr-1", RoomID: "r1", ParentMessageID: "p1", LastMsgID: "m1", SiteID: "site-a", LastMsgAt: base.Add(5 * time.Hour), CreatedAt: base, UpdatedAt: base})
	insertThreadRoom(t, db, model.ThreadRoom{ID: "tr-2", RoomID: "r2", ParentMessageID: "p2", LastMsgID: "m2", SiteID: "site-a", LastMsgAt: base.Add(3 * time.Hour), CreatedAt: base, UpdatedAt: base})
	insertThreadRoom(t, db, model.ThreadRoom{ID: "tr-3", RoomID: "r1", ParentMessageID: "p3", LastMsgID: "m3", SiteID: "site-a", LastMsgAt: base.Add(1 * time.Hour), CreatedAt: base, UpdatedAt: base})

	insertThreadSubscription(t, db, model.ThreadSubscription{ID: "ts-1", ThreadRoomID: "tr-1", RoomID: "r1", ParentMessageID: "p1", UserAccount: "alice", SiteID: "site-a", LastSeenAt: timePtr(base.Add(2 * time.Hour)), HasMention: true, CreatedAt: base, UpdatedAt: base})
	insertThreadSubscription(t, db, model.ThreadSubscription{ID: "ts-2", ThreadRoomID: "tr-2", RoomID: "r2", ParentMessageID: "p2", UserAccount: "alice", SiteID: "site-a", LastSeenAt: nil, CreatedAt: base, UpdatedAt: base})
	insertThreadSubscription(t, db, model.ThreadSubscription{ID: "ts-3", ThreadRoomID: "tr-3", RoomID: "r1", ParentMessageID: "p3", UserAccount: "alice", SiteID: "site-a", LastSeenAt: timePtr(base.Add(1 * time.Hour)), CreatedAt: base, UpdatedAt: base})
	// bob's sub — must never appear for alice.
	insertThreadSubscription(t, db, model.ThreadSubscription{ID: "ts-bob", ThreadRoomID: "tr-1", RoomID: "r1", ParentMessageID: "p1", UserAccount: "bob", SiteID: "site-a", CreatedAt: base, UpdatedAt: base})
	// orphan sub — thread_room missing, $unwind drops it.
	insertThreadSubscription(t, db, model.ThreadSubscription{ID: "ts-orphan", ThreadRoomID: "tr-gone", RoomID: "r1", ParentMessageID: "p9", UserAccount: "alice", SiteID: "site-a", CreatedAt: base, UpdatedAt: base})

	// rooms feed the name/type $lookup; r2 is intentionally unseeded to exercise
	// the missing-room degrade (empty name/type, row still returned).
	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{ID: "r1", Name: "general", Type: model.RoomTypeChannel, SiteID: "site-a"})
	require.NoError(t, err)

	// Page 1, limit 2: newest two (tr-1 5h, tr-2 3h), hasMore true.
	rows, hasMore, err := repo.ListUserThreadSubscriptions(ctx, "alice", nil, "", 2)
	require.NoError(t, err)
	assert.True(t, hasMore)
	require.Len(t, rows, 2)
	assert.Equal(t, "tr-1", rows[0].ThreadRoomID)
	assert.Equal(t, "tr-2", rows[1].ThreadRoomID)

	// Joined + own fields land on the row.
	assert.Equal(t, "r1", rows[0].RoomID)
	assert.Equal(t, "site-a", rows[0].SiteID)
	assert.Equal(t, "p1", rows[0].ParentMessageID)
	assert.Equal(t, "m1", rows[0].LastMsgID)
	assert.True(t, rows[0].HasMention)
	require.NotNil(t, rows[0].LastSeenAt)
	assert.WithinDuration(t, base.Add(2*time.Hour), *rows[0].LastSeenAt, time.Second)
	assert.Nil(t, rows[1].LastSeenAt) // never-seen thread

	// Room name/type ride in via the rooms $lookup; r2 unseeded ⇒ degrade to empty.
	assert.Equal(t, "general", rows[0].RoomName)
	assert.Equal(t, model.RoomTypeChannel, rows[0].RoomType)
	assert.Empty(t, rows[1].RoomName)
	assert.Empty(t, string(rows[1].RoomType))

	// Page 2 from the cursor at row[1]: only tr-3 remains, hasMore false.
	last := rows[1]
	rows2, hasMore2, err := repo.ListUserThreadSubscriptions(ctx, "alice", &last.LastMsgAt, last.ThreadRoomID, 2)
	require.NoError(t, err)
	assert.False(t, hasMore2)
	require.Len(t, rows2, 1)
	assert.Equal(t, "tr-3", rows2[0].ThreadRoomID)

	// Unknown account → empty.
	none, hasMoreNone, err := repo.ListUserThreadSubscriptions(ctx, "nobody", nil, "", 10)
	require.NoError(t, err)
	assert.False(t, hasMoreNone)
	assert.Empty(t, none)
}

// Equal lastMsgAt across threads is fully ordered by threadRoomId DESC, so a
// page boundary neither skips nor duplicates.
func TestThreadSubscriptionRepo_ListUserThreadSubscriptions_Tiebreak(t *testing.T) {
	db := setupMongo(t)
	repo := NewThreadSubscriptionRepo(db)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	same := base.Add(5 * time.Hour)

	for _, id := range []string{"tr-a", "tr-b", "tr-c"} {
		insertThreadRoom(t, db, model.ThreadRoom{ID: id, RoomID: "r1", ParentMessageID: "p-" + id, SiteID: "site-a", LastMsgAt: same, CreatedAt: base, UpdatedAt: base})
		insertThreadSubscription(t, db, model.ThreadSubscription{ID: "ts-" + id, ThreadRoomID: id, RoomID: "r1", ParentMessageID: "p-" + id, UserAccount: "alice", SiteID: "site-a", CreatedAt: base, UpdatedAt: base})
	}

	// threadRoomId DESC ⇒ tr-c, tr-b, tr-a.
	rows, hasMore, err := repo.ListUserThreadSubscriptions(ctx, "alice", nil, "", 2)
	require.NoError(t, err)
	assert.True(t, hasMore)
	require.Len(t, rows, 2)
	assert.Equal(t, "tr-c", rows[0].ThreadRoomID)
	assert.Equal(t, "tr-b", rows[1].ThreadRoomID)

	rows2, hasMore2, err := repo.ListUserThreadSubscriptions(ctx, "alice", &rows[1].LastMsgAt, rows[1].ThreadRoomID, 2)
	require.NoError(t, err)
	assert.False(t, hasMore2)
	require.Len(t, rows2, 1)
	assert.Equal(t, "tr-a", rows2[0].ThreadRoomID)
}
