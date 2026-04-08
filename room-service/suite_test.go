//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var (
	sharedMongoClient *mongo.Client
	sharedNATSConn    *otelnats.Conn
	dbCounter         atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Start shared MongoDB container
	mongoContainer, err := mongodb.Run(ctx, "mongo:4.4.15")
	if err != nil {
		fmt.Fprintf(os.Stderr, "start mongo: %v\n", err)
		os.Exit(1)
	}

	mongoURI, err := mongoContainer.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "get mongo uri: %v\n", err)
		os.Exit(1)
	}

	sharedMongoClient, err = mongo.Connect(options.Client().ApplyURI(mongoURI))
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect mongo: %v\n", err)
		os.Exit(1)
	}

	// Start shared NATS container
	natsContainer, err := tcnats.Run(ctx, "nats:2.10-alpine")
	if err != nil {
		fmt.Fprintf(os.Stderr, "start nats: %v\n", err)
		os.Exit(1)
	}

	natsURI, err := natsContainer.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "get nats uri: %v\n", err)
		os.Exit(1)
	}

	sharedNATSConn, err = otelnats.Connect(natsURI)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect nats: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	// Cleanup
	sharedNATSConn.Drain()
	sharedMongoClient.Disconnect(ctx)
	mongoContainer.Terminate(ctx)
	natsContainer.Terminate(ctx)

	os.Exit(code)
}

// freshDB returns a unique database for each test to ensure isolation.
func freshDB(t *testing.T) *mongo.Database {
	t.Helper()
	n := dbCounter.Add(1)
	return sharedMongoClient.Database(fmt.Sprintf("test_%d", n))
}
