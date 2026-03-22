package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/cassutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
)

type config struct {
	NatsURL           string `env:"NATS_URL"           envDefault:"nats://localhost:4222"`
	SiteID            string `env:"SITE_ID"            envDefault:"site-local"`
	MongoURI          string `env:"MONGO_URI"          envDefault:"mongodb://localhost:27017"`
	MongoDB           string `env:"MONGO_DB"           envDefault:"chat"`
	CassandraHosts    string `env:"CASSANDRA_HOSTS"    envDefault:"localhost"`
	CassandraKeyspace string `env:"CASSANDRA_KEYSPACE" envDefault:"chat"`
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

	cassSession, err := cassutil.Connect(strings.Split(cfg.CassandraHosts, ","), cfg.CassandraKeyspace)
	if err != nil {
		slog.Error("cassandra connect failed", "error", err)
		os.Exit(1)
	}

	store := NewMongoStore(db, cassSession)
	handler := NewHandler(store, cfg.SiteID, func(subj string, data []byte) error {
		return nc.Publish(subj, data)
	})

	streamCfg := stream.Messages(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamCfg.Name,
		Subjects: streamCfg.Subjects,
	}); err != nil {
		slog.Error("create stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, streamCfg.Name, jetstream.ConsumerConfig{
		Durable:   "message-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		slog.Error("create consumer failed", "error", err)
		os.Exit(1)
	}

	cctx, err := cons.Consume(handler.HandleJetStreamMsg)
	if err != nil {
		slog.Error("consume failed", "error", err)
		os.Exit(1)
	}

	slog.Info("message-worker running", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error {
			cctx.Drain()
			select {
			case <-cctx.Closed():
				return nil
			case <-ctx.Done():
				return fmt.Errorf("consumer drain timed out: %w", ctx.Err())
			}
		},
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
	)
}
