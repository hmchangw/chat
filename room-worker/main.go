package main

import (
	"context"
	"log"
	"os"

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

	ctx := context.Background()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("nats: %v", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	mongoClient, err := mongoutil.Connect(ctx, mongoURI)
	if err != nil {
		log.Fatalf("mongo: %v", err)
	}

	streamCfg := stream.Rooms(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: streamCfg.Name, Subjects: streamCfg.Subjects,
	})
	if err != nil {
		log.Fatalf("create stream: %v", err)
	}

	store := NewMongoStore(mongoClient.Database(mongoDB))
	handler := NewHandler(store, siteID, func(subj string, data []byte) error {
		return nc.Publish(subj, data)
	})

	cons, err := js.CreateOrUpdateConsumer(ctx, streamCfg.Name, jetstream.ConsumerConfig{
		Durable: "room-worker", AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}
	cons.Consume(handler.HandleJetStreamMsg)

	log.Printf("room-worker running (site=%s)", siteID)

	shutdown.Wait(ctx,
		func(ctx context.Context) error { nc.Drain(); return nil },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
