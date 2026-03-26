package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/config"
	"github.com/hmchangw/chat/history-service/internal/mongorepo"
	"github.com/hmchangw/chat/history-service/internal/natshandler"
	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/pkg/cassutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	nc, err := nats.Connect(cfg.NATS.URL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}

	mongoClient, err := mongoutil.Connect(ctx, cfg.Mongo.URI)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}

	cassSession, err := cassutil.Connect(strings.Split(cfg.Cassandra.Hosts, ","), cfg.Cassandra.Keyspace)
	if err != nil {
		slog.Error("cassandra connect failed", "error", err)
		os.Exit(1)
	}

	cassRepo := cassrepo.NewRepository(cassSession)
	mongoRepo := mongorepo.NewRepository(mongoClient.Database(cfg.Mongo.DB))
	svc := service.New(cassRepo, mongoRepo)
	handler := natshandler.New(svc, cfg.SiteID)

	if err := handler.Register(nc); err != nil {
		slog.Error("register handlers failed", "error", err)
		os.Exit(1)
	}

	slog.Info("history-service running", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
	)
}
