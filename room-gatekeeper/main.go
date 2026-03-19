package main

import (
	"context"
	"log"
	"os"
	"strconv"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	natsURL := envOr("NATS_URL", nats.DefaultURL)
	siteID := envOr("SITE_ID", "site-local")
	mongoURI := envOr("MONGO_URI", "mongodb://localhost:27017")
	mongoDB := envOr("MONGO_DB", "chat")
	maxRoomSize, _ := strconv.Atoi(envOr("MAX_ROOM_SIZE", "1000"))

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
	db := mongoClient.Database(mongoDB)

	// Ensure ROOMS stream exists
	streamCfg := stream.Rooms(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: streamCfg.Name, Subjects: streamCfg.Subjects,
	})
	if err != nil {
		log.Fatalf("create stream: %v", err)
	}

	store := NewMongoStore(db)
	handler := NewHandler(store, siteID, maxRoomSize, func(data []byte) error {
		_, err := js.Publish(ctx, subject.MemberInviteWildcard(siteID), data)
		return err
	})

	// Register CRUD handlers
	if err := handler.RegisterCRUD(nc); err != nil {
		log.Fatalf("register: %v", err)
	}

	// Subscribe to invite requests
	inviteSubj := subject.MemberInviteWildcard(siteID)
	if _, err := nc.Subscribe(inviteSubj, handler.NatsHandleInvite); err != nil {
		log.Fatalf("subscribe invite: %v", err)
	}

	log.Printf("room-gatekeeper running (site=%s)", siteID)

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
