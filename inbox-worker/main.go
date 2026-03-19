package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/caarlos0/env/v11"
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

type config struct {
	NatsURL  string `env:"NATS_URL"  envDefault:"nats://localhost:4222"`
	SiteID   string `env:"SITE_ID"   envDefault:"default"`
	MongoURI string `env:"MONGO_URI" envDefault:"mongodb://localhost:27017"`
	MongoDB  string `env:"MONGO_DB"  envDefault:"chat"`
}

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

func main() {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}
	db := mongoClient.Database(cfg.MongoDB)
	store := &mongoInboxStore{
		subCol:  db.Collection("subscriptions"),
		roomCol: db.Collection("rooms"),
	}

	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("jetstream init failed", "error", err)
		os.Exit(1)
	}

	inboxCfg := stream.Inbox(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: inboxCfg.Name,
	}); err != nil {
		slog.Error("create inbox stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, inboxCfg.Name, jetstream.ConsumerConfig{
		Durable:   "inbox-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		slog.Error("create consumer failed", "error", err)
		os.Exit(1)
	}

	publisher := &natsPublisher{nc: nc}
	handler := NewHandler(store, publisher)

	cctx, err := cons.Consume(func(msg jetstream.Msg) {
		if err := handler.HandleEvent(ctx, msg.Data()); err != nil {
			slog.Error("handle event failed", "error", err)
			msg.Nak()
			return
		}
		msg.Ack()
	})
	if err != nil {
		slog.Error("consume failed", "error", err)
		os.Exit(1)
	}

	slog.Info("inbox-worker started", "site", cfg.SiteID)

	shutdown.Wait(ctx,
		func(ctx context.Context) error { cctx.Stop(); return nil },
		func(ctx context.Context) error { nc.Drain(); return nil },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
}

// natsPublisher adapts *nats.Conn to the Publisher interface.
type natsPublisher struct {
	nc *nats.Conn
}

func (p *natsPublisher) Publish(subject string, data []byte) error {
	return p.nc.Publish(subject, data)
}
