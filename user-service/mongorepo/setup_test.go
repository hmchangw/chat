//go:build integration

package mongorepo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/testutil"
	"github.com/hmchangw/chat/user-service/service"
)

// Compile-time assertions that each repo satisfies its service-defined
// interface. This is the primary correctness gate when Docker (and thus the
// integration tests) is unavailable: `go vet -tags integration` fails here if
// any method signature drifts from the interfaces.
var (
	_ service.SubscriptionRepository = (*SubscriptionRepo)(nil)
	_ service.UserRepository         = (*UserRepo)(nil)
	_ service.AppRepository          = (*AppRepo)(nil)
)

// newTestSubscriptionRepo builds a SubscriptionRepo over an isolated test
// database with the local site fixed to "site-a". Seed local rows with siteId
// "site-a" and cross-site rows with another siteId (e.g. "site-b") to exercise
// the deleted-filter.
func newTestSubscriptionRepo(t *testing.T) (*SubscriptionRepo, *mongo.Database) {
	t.Helper()
	db := testutil.MongoDB(t, "user-service")
	r := NewSubscriptionRepo(db, "site-a")
	require.NoError(t, r.EnsureIndexes(context.Background()))
	return r, db
}

// newTestUserRepo builds a UserRepo over an isolated test database.
func newTestUserRepo(t *testing.T) (*UserRepo, *mongo.Database) {
	t.Helper()
	db := testutil.MongoDB(t, "user-service")
	r := NewUserRepo(db)
	require.NoError(t, r.EnsureIndexes(context.Background()))
	return r, db
}

// newTestAppRepo builds an AppRepo over an isolated test database.
func newTestAppRepo(t *testing.T) (*AppRepo, *mongo.Database) {
	t.Helper()
	db := testutil.MongoDB(t, "user-service")
	return NewAppRepo(db), db
}

// seed inserts raw docs into a collection on db.
func seed(t *testing.T, db *mongo.Database, coll string, docs ...any) {
	t.Helper()
	_, err := db.Collection(coll).InsertMany(context.Background(), docs)
	require.NoError(t, err)
}
