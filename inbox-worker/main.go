package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoInboxStore implements InboxStore using MongoDB.
type mongoInboxStore struct {
	subCol  *mongo.Collection
	roomCol *mongo.Collection
}

func (s *mongoInboxStore) CreateSubscription(ctx context.Context, sub model.Subscription) error {
	_, err := s.subCol.InsertOne(ctx, sub)
	return err
}

func (s *mongoInboxStore) UpsertRoom(ctx context.Context, room model.Room) error {
	filter := bson.M{"_id": room.ID}
	update := bson.M{"$set": room}
	opts := options.UpdateOne().SetUpsert(true)
	_, err := s.roomCol.UpdateOne(ctx, filter, update, opts)
	return err
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	siteID := envOrDefault("SITE_ID", "default")
	mongoURI := envOrDefault("MONGO_URI", "mongodb://localhost:27017")
	mongoDB := envOrDefault("MONGO_DB", "chat")

	ctx := context.Background()

	// --- MongoDB ---
	mongoClient, err := mongoutil.Connect(ctx, mongoURI)
	if err != nil {
		log.Fatalf("mongo: %v", err)
	}
	db := mongoClient.Database(mongoDB)
	store := &mongoInboxStore{
		subCol:  db.Collection("subscriptions"),
		roomCol: db.Collection("rooms"),
	}

	// --- NATS ---
	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("nats connect: %v", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	// --- Ensure INBOX stream exists ---
	// The INBOX stream is configured with Sources from remote OUTBOX streams.
	// Stream creation with sources is typically done by infrastructure/provisioning.
	// Here we just ensure the stream exists for the consumer to attach to.
	inboxCfg := stream.Inbox(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: inboxCfg.Name,
	})
	if err != nil {
		log.Fatalf("create inbox stream: %v", err)
	}

	// --- Create pull consumer ---
	cons, err := js.CreateOrUpdateConsumer(ctx, inboxCfg.Name, jetstream.ConsumerConfig{
		Durable:   "inbox-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}

	// --- Publisher wraps *nats.Conn ---
	publisher := &natsPublisher{nc: nc}

	handler := NewHandler(store, publisher)

	log.Printf("inbox-worker started (site=%s)", siteID)

	// --- Consume loop ---
	go func() {
		for {
			msgs, err := cons.Fetch(10, jetstream.FetchMaxWait(5*time.Second))
			if err != nil {
				if err == context.Canceled {
					return
				}
				log.Printf("fetch: %v", err)
				continue
			}
			for msg := range msgs.Messages() {
				if err := handler.HandleEvent(ctx, msg.Data()); err != nil {
					log.Printf("handle error: %v", err)
					msg.Nak()
					continue
				}
				msg.Ack()
			}
			if msgs.Error() != nil {
				log.Printf("fetch iteration error: %v", msgs.Error())
			}
		}
	}()

	// --- Graceful shutdown ---
	shutdown.Wait(ctx,
		func(ctx context.Context) error {
			nc.Close()
			return nil
		},
		func(ctx context.Context) error {
			mongoutil.Disconnect(ctx, mongoClient)
			return nil
		},
	)
}

// natsPublisher adapts *nats.Conn to the Publisher interface.
type natsPublisher struct {
	nc *nats.Conn
}

func (p *natsPublisher) Publish(subject string, data []byte) error {
	return p.nc.Publish(subject, data)
}
