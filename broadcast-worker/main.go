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
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// mongoRoomLookup implements RoomLookup using MongoDB.
type mongoRoomLookup struct {
	roomCol *mongo.Collection
	subCol  *mongo.Collection
}

func (m *mongoRoomLookup) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	filter := map[string]string{"_id": roomID}
	var room model.Room
	err := m.roomCol.FindOne(ctx, filter).Decode(&room)
	if err != nil {
		return nil, err
	}
	return &room, nil
}

func (m *mongoRoomLookup) ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error) {
	filter := map[string]string{"roomId": roomID}
	cursor, err := m.subCol.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, err
	}
	return subs, nil
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
	roomLookup := &mongoRoomLookup{
		roomCol: db.Collection("rooms"),
		subCol:  db.Collection("subscriptions"),
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

	// --- Ensure FANOUT stream exists ---
	fanoutCfg := stream.Fanout(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     fanoutCfg.Name,
		Subjects: fanoutCfg.Subjects,
	})
	if err != nil {
		log.Fatalf("create fanout stream: %v", err)
	}

	// --- Create pull consumer ---
	cons, err := js.CreateOrUpdateConsumer(ctx, fanoutCfg.Name, jetstream.ConsumerConfig{
		Durable:   "broadcast-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("create consumer: %v", err)
	}

	// --- Publisher wraps *nats.Conn ---
	publisher := &natsPublisher{nc: nc}

	handler := NewHandler(roomLookup, publisher)

	log.Printf("broadcast-worker started (site=%s)", siteID)

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
				if err := handler.HandleMessage(ctx, msg.Data()); err != nil {
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
