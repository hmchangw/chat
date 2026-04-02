package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go/jetstream"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"
	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
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

func (s *mongoInboxStore) CreateSubscription(ctx context.Context, sub *model.Subscription) error {
	_, err := s.subCol.InsertOne(ctx, sub)
	return err
}

func (s *mongoInboxStore) UpsertRoom(ctx context.Context, room *model.Room) error {
	filter := bson.M{"_id": room.ID}
	update := bson.M{"$set": room}
	opts := options.UpdateOne().SetUpsert(true)
	_, err := s.roomCol.UpdateOne(ctx, filter, update, opts)
	return err
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	tracerShutdown, err := otelutil.InitTracer(ctx, "inbox-worker")
	if err != nil {
		slog.Error("init tracer failed", "error", err)
		os.Exit(1)
	}

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

	nc, err := otelnats.Connect(cfg.NatsURL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}

	js, err := oteljetstream.New(nc)
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

	cctx, err := cons.Consume(func(m oteljetstream.MsgWithContext) {
		if err := handler.HandleEvent(m.Context(), m.Data()); err != nil {
			slog.Error("handle event failed", "error", err)
			if err := m.Nak(); err != nil {
				slog.Error("failed to nak message", "error", err)
			}
			return
		}
		if err := m.Ack(); err != nil {
			slog.Error("failed to ack message", "error", err)
		}
	})
	if err != nil {
		slog.Error("consume failed", "error", err)
		os.Exit(1)
	}

	slog.Info("inbox-worker started", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error {
			cctx.Stop()
			return nil
		},
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
}

// natsPublisher adapts *otelnats.Conn to the Publisher interface.
type natsPublisher struct {
	nc *otelnats.Conn
}

func (p *natsPublisher) Publish(ctx context.Context, subject string, data []byte) error {
	return p.nc.Publish(ctx, subject, data)
}
