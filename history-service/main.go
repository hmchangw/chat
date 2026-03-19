package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/hmchangw/chat/pkg/cassutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/nats-io/nats.go"
)

func main() {
	natsURL := envOr("NATS_URL", nats.DefaultURL)
	siteID := envOr("SITE_ID", "site-local")
	mongoURI := envOr("MONGO_URI", "mongodb://localhost:27017")
	mongoDB := envOr("MONGO_DB", "chat")
	cassHosts := envOr("CASSANDRA_HOSTS", "localhost")
	cassKeyspace := envOr("CASSANDRA_KEYSPACE", "chat")

	ctx := context.Background()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("nats: %v", err)
	}

	mongoClient, err := mongoutil.Connect(ctx, mongoURI)
	if err != nil {
		log.Fatalf("mongo: %v", err)
	}

	cassSession, err := cassutil.Connect(strings.Split(cassHosts, ","), cassKeyspace)
	if err != nil {
		log.Fatalf("cassandra: %v", err)
	}

	store := NewRealStore(mongoClient.Database(mongoDB), cassSession)
	handler := NewHandler(store)

	histSubj := subject.MsgHistoryWildcard(siteID)
	if _, err := nc.Subscribe(histSubj, handler.NatsHandleHistory); err != nil {
		log.Fatalf("subscribe: %v", err)
	}

	log.Printf("history-service running (site=%s)", siteID)

	shutdown.Wait(ctx,
		func(ctx context.Context) error { nc.Drain(); return nil },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
	)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
