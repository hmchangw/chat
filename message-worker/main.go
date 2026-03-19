package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/hmchangw/chat/pkg/cassutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	natsURL := envOr("NATS_URL", nats.DefaultURL)
	siteID := envOr("SITE_ID", "site-local")
	mongoURI := envOr("MONGO_URI", "mongodb://localhost:27017")
	mongoDB := envOr("MONGO_DB", "chat")
	cassHosts := envOr("CASSANDRA_HOSTS", "localhost")
	cassKeyspace := envOr("CASSANDRA_KEYSPACE", "chat")

	ctx := context.Background()

	// Connect to NATS
	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("nats connect: %v", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	// Connect to MongoDB
	mongoClient, err := mongoutil.Connect(ctx, mongoURI)
	if err != nil {
		log.Fatalf("mongo: %v", err)
	}
	db := mongoClient.Database(mongoDB)

	// Connect to Cassandra
	cassSession, err := cassutil.Connect(strings.Split(cassHosts, ","), cassKeyspace)
	if err != nil {
		log.Fatalf("cassandra: %v", err)
	}

	// Create store and handler
	store := NewMongoStore(db, cassSession)
	handler := NewHandler(store, siteID, func(subj string, data []byte) error {
		return nc.Publish(subj, data)
	})

	// Ensure stream exists
	streamCfg := stream.Messages(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamCfg.Name,
		Subjects: streamCfg.Subjects,
	})
	if err != nil {
		log.Fatalf("create stream: %v", err)
	}

	// Create pull consumer
	cons, err := js.CreateOrUpdateConsumer(ctx, streamCfg.Name, jetstream.ConsumerConfig{
		Durable:   "message-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}

	// Start consuming
	_, err = cons.Consume(handler.HandleJetStreamMsg)
	if err != nil {
		log.Fatalf("consume: %v", err)
	}

	log.Printf("message-worker running (site=%s)", siteID)

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
