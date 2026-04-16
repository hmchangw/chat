package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/roomkeystore"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
)

type config struct {
	NatsURL           string        `env:"NATS_URL"                  envDefault:"nats://localhost:4222"`
	NatsCredsFile     string        `env:"NATS_CREDS_FILE"           envDefault:""`
	SiteID            string        `env:"SITE_ID"                   envDefault:"site-local"`
	MongoURI          string        `env:"MONGO_URI"                 envDefault:"mongodb://localhost:27017"`
	MongoDB           string        `env:"MONGO_DB"                  envDefault:"chat"`
	MaxRoomSize       int           `env:"MAX_ROOM_SIZE"             envDefault:"1000"`
	MaxBatchSize      int           `env:"MAX_BATCH_SIZE"            envDefault:"500"`
	ValkeyAddr        string        `env:"VALKEY_ADDR,required"`
	ValkeyPassword    string        `env:"VALKEY_PASSWORD"           envDefault:""`
	ValkeyGracePeriod time.Duration `env:"VALKEY_KEY_GRACE_PERIOD,required"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	tracerShutdown, err := otelutil.InitTracer(ctx, "room-service")
	if err != nil {
		slog.Error("init tracer failed", "error", err)
		os.Exit(1)
	}

	nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}
	js, err := oteljetstream.New(nc)
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

	keyStore, err := roomkeystore.NewValkeyStore(roomkeystore.Config{
		Addr:        cfg.ValkeyAddr,
		Password:    cfg.ValkeyPassword,
		GracePeriod: cfg.ValkeyGracePeriod,
	})
	if err != nil {
		slog.Error("valkey connect failed", "error", err)
		os.Exit(1)
	}

	streamCfg := stream.Rooms(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: streamCfg.Name, Subjects: streamCfg.Subjects,
	}); err != nil {
		slog.Error("create stream failed", "error", err)
		os.Exit(1)
	}

	store := NewMongoStore(db)
	handler := NewHandler(store, keyStore, cfg.SiteID, cfg.MaxRoomSize, cfg.MaxBatchSize, func(ctx context.Context, subj string, data []byte) error {
		if _, err := js.Publish(ctx, subj, data); err != nil {
			return fmt.Errorf("publish to %q: %w", subj, err)
		}
		return nil
	})

	if err := handler.RegisterCRUD(nc); err != nil {
		slog.Error("register CRUD handlers failed", "error", err)
		os.Exit(1)
	}

	slog.Info("room-service running", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
}
