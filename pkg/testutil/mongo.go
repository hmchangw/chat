//go:build integration

package testutil

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

var (
	mongoOnce      sync.Once
	mongoClient    *mongo.Client
	mongoContainer testcontainers.Container
	mongoInitErr   error
)

func ensureMongoClient() (*mongo.Client, error) {
	mongoOnce.Do(func() {
		ctx := context.Background()
		container, err := mongodb.Run(ctx, testimages.Mongo)
		if err != nil {
			mongoInitErr = fmt.Errorf("start mongo: %w", err)
			return
		}
		uri, err := container.ConnectionString(ctx)
		if err != nil {
			_ = container.Terminate(ctx)
			mongoInitErr = fmt.Errorf("get mongo uri: %w", err)
			return
		}
		c, err := mongo.Connect(options.Client().ApplyURI(uri))
		if err != nil {
			_ = container.Terminate(ctx)
			mongoInitErr = fmt.Errorf("connect mongo: %w", err)
			return
		}
		mongoClient = c
		mongoContainer = container
	})
	return mongoClient, mongoInitErr
}

// TerminateMongo disconnects the shared client and stops the shared
// container. Best-effort; errors go to stderr. Intended for TestMain to
// call on clean exits when Ryuk is disabled (e.g., in CI).
func TerminateMongo() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if mongoClient != nil {
		if err := mongoClient.Disconnect(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "disconnect shared mongo client: %v\n", err)
		}
	}
	if mongoContainer != nil {
		if err := mongoContainer.Terminate(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "terminate shared mongo: %v\n", err)
		}
	}
}

// MongoDB returns an isolated Mongo database for the current test; dropped on t.Cleanup.
func MongoDB(t *testing.T, prefix string) *mongo.Database {
	t.Helper()
	c, err := ensureMongoClient()
	if err != nil {
		t.Fatalf("testutil.MongoDB: %v", err)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(t.Name())) // hash.Hash.Write never returns an error.
	db := c.Database(fmt.Sprintf("%s_%x", prefix, h.Sum64()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = db.Drop(ctx)
	})
	return db
}
