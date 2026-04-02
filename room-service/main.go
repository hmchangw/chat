package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

type config struct {
	NatsURL       string `env:"NATS_URL"        envRequired:"true"`
	SiteID        string `env:"SITE_ID"         envDefault:"site-local"`
	MongoURI      string `env:"MONGO_URI"       envRequired:"true"`
	MongoDB       string `env:"MONGO_DB"        envDefault:"chat"`
	MaxRoomSize   int    `env:"MAX_ROOM_SIZE"   envDefault:"1000"`
	CurrentDomain string `env:"CURRENT_DOMAIN"  envRequired:"true"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

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

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}
	db := mongoClient.Database(cfg.MongoDB)

	// Create ROOMS stream for invite processing
	roomsStreamCfg := stream.Rooms(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: roomsStreamCfg.Name, Subjects: roomsStreamCfg.Subjects,
	}); err != nil {
		slog.Error("create rooms stream failed", "error", err)
		os.Exit(1)
	}

	// Create OUTBOX stream for cross-site federation events
	outboxStreamCfg := stream.Outbox(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: outboxStreamCfg.Name, Subjects: outboxStreamCfg.Subjects,
	}); err != nil {
		slog.Error("create outbox stream failed", "error", err)
		os.Exit(1)
	}

	store := NewMongoStore(db)

	if err := store.EnsureIndexes(ctx); err != nil {
		slog.Error("ensure indexes failed", "error", err)
		os.Exit(1)
	}

	handler := NewHandler(store, cfg.SiteID, cfg.CurrentDomain, cfg.MaxRoomSize,
		// publishToStream: publish to ROOMS JetStream stream for room-worker processing
		func(subj string, data []byte) error {
			_, err := js.Publish(ctx, subj, data)
			return err
		},
		// publishLocal: publish directly to NATS for local user notifications
		func(subj string, data []byte) error { return nc.Publish(subj, data) },
		// publishOutbox: publish to OUTBOX JetStream stream for cross-site federation
		func(subj string, data []byte) error { _, err := js.Publish(ctx, subj, data); return err },
	)

	handler.StartNotificationWorkers(10)

	if err := handler.RegisterCRUD(nc); err != nil {
		slog.Error("register CRUD handlers failed", "error", err)
		os.Exit(1)
	}

	inviteSubj := subject.MemberInviteWildcard(cfg.SiteID)
	if _, err := nc.QueueSubscribe(inviteSubj, "room-service", handler.NatsHandleInvite); err != nil {
		slog.Error("subscribe invite failed", "error", err)
		os.Exit(1)
	}

	slog.Info("room-service running", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error { handler.StopNotificationWorkers(); return nil },
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
}
