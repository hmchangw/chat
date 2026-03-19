package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/caarlos0/env/v11"
	"github.com/hmchangw/chat/pkg/cassutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/nats-io/nats.go"
)

type config struct {
	NatsURL           string `env:"NATS_URL"            envDefault:"nats://localhost:4222"`
	SiteID            string `env:"SITE_ID"             envDefault:"site-local"`
	MongoURI          string `env:"MONGO_URI"           envDefault:"mongodb://localhost:27017"`
	MongoDB           string `env:"MONGO_DB"            envDefault:"chat"`
	CassandraHosts    string `env:"CASSANDRA_HOSTS"     envDefault:"localhost"`
	CassandraKeyspace string `env:"CASSANDRA_KEYSPACE"  envDefault:"chat"`
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

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}

	cassSession, err := cassutil.Connect(strings.Split(cfg.CassandraHosts, ","), cfg.CassandraKeyspace)
	if err != nil {
		slog.Error("cassandra connect failed", "error", err)
		os.Exit(1)
	}

	store := NewRealStore(mongoClient.Database(cfg.MongoDB), cassSession)
	handler := NewHandler(store)

	histSubj := subject.MsgHistoryWildcard(cfg.SiteID)
	if _, err := nc.QueueSubscribe(histSubj, "history-service", handler.NatsHandleHistory); err != nil {
		slog.Error("subscribe failed", "error", err)
		os.Exit(1)
	}

	slog.Info("history-service running", "site", cfg.SiteID)

	shutdown.Wait(ctx,
		func(ctx context.Context) error { nc.Drain(); return nil },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
	)
}
