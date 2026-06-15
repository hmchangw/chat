//go:build integration

package mongorepo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/testutil"
)

// indexKeySpecs returns the set of index key specs on coll, each rendered as a
// deterministic "field:dir,..." string that preserves key order (bson.D), so
// tests assert the exact compound index rather than a count that an unrelated
// index could satisfy.
func indexKeySpecs(t *testing.T, coll *mongo.Collection) map[string]struct{} {
	t.Helper()
	cur, err := coll.Indexes().List(context.Background())
	require.NoError(t, err)
	var idx []struct {
		Key bson.D `bson:"key"`
	}
	require.NoError(t, cur.All(context.Background(), &idx))
	out := make(map[string]struct{}, len(idx))
	for _, ix := range idx {
		parts := make([]string, 0, len(ix.Key))
		for _, e := range ix.Key {
			parts = append(parts, fmt.Sprintf("%s:%v", e.Key, e.Value))
		}
		out[strings.Join(parts, ",")] = struct{}{}
	}
	return out
}

func TestEnsureIndexes_Integration(t *testing.T) {
	subRepo, _ := newTestSubscriptionRepo(t)
	userRepo, _ := newTestUserRepo(t)

	subKeys := indexKeySpecs(t, subRepo.subscriptions.Raw())
	// {u.account, roomType} serves the account+roomType match on every list/count
	// path. (The retention window keys on room.lastMsgAt, not a subscription field.)
	require.Contains(t, subKeys, "u.account:1,roomType:1")
	require.Contains(t, subKeys, "roomId:1,u.account:1")
	require.Contains(t, subKeys, "name:1,roomType:1")

	userKeys := indexKeySpecs(t, userRepo.users.Raw())
	require.Contains(t, userKeys, "account:1")
}

// A user holds at most one subscription per room: (roomId, u.account) is the
// unique logical key, matching room-service's declaration on the shared
// collection. A second doc with the same pair must hit the unique index.
func TestSubscriptionUniqueIndex_Integration(t *testing.T) {
	subRepo, _ := newTestSubscriptionRepo(t)
	ctx := context.Background()
	col := subRepo.subscriptions.Raw()
	doc := func(id string) bson.M {
		return bson.M{"_id": id, "roomId": "r1", "u": bson.M{"account": "alice"}}
	}
	_, err := col.InsertOne(ctx, doc("sub-1"))
	require.NoError(t, err)
	_, err = col.InsertOne(ctx, doc("sub-2"))
	require.True(t, mongo.IsDuplicateKeyError(err), "expected duplicate-key error, got %v", err)
}

// EnsureIndexes shares the unique account index with room-service. When the
// collection already carries duplicate accounts, the unique-index build fails
// E11000 and the operator must be pointed at the one-time dedupe preflight.
func TestUserEnsureIndexes_DuplicateAccounts_Integration(t *testing.T) {
	db := testutil.MongoDB(t, "user-service")
	ctx := context.Background()
	seed(t, db, usersCollection,
		bson.M{"_id": "u1", "account": "dup"},
		bson.M{"_id": "u2", "account": "dup"},
	)
	err := NewUserRepo(db).EnsureIndexes(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "dedupe preflight")
}

// A pre-existing NON-unique account_1 index can't be upgraded in place; the
// error must tell the operator to drop it (85 IndexOptionsConflict).
func TestUserEnsureIndexes_NonUniqueAccountConflict_Integration(t *testing.T) {
	db := testutil.MongoDB(t, "user-service")
	ctx := context.Background()
	_, err := db.Collection(usersCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "account", Value: 1}},
	})
	require.NoError(t, err)
	err = NewUserRepo(db).EnsureIndexes(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "drop the old non-unique account_1 index")
}
