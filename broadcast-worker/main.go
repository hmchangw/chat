package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"
	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/pkg/cachestats"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/roomkeystore"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/userstore"
)

type encryptionConfig struct {
	Enabled bool `env:"ENABLED" envDefault:"false"`
}

type config struct {
	NatsURL               string                  `env:"NATS_URL"                envDefault:"nats://localhost:4222"`
	NatsCredsFile         string                  `env:"NATS_CREDS_FILE"         envDefault:""`
	SiteID                string                  `env:"SITE_ID"                 envDefault:"default"`
	MongoURI              string                  `env:"MONGO_URI"               envDefault:"mongodb://localhost:27017"`
	MongoDB               string                  `env:"MONGO_DB"                envDefault:"chat"`
	MongoUsername         string                  `env:"MONGO_USERNAME"          envDefault:""`
	MongoPassword         string                  `env:"MONGO_PASSWORD"          envDefault:""`
	MaxWorkers            int                     `env:"MAX_WORKERS"             envDefault:"100"`
	UserCacheSize         int                     `env:"USER_CACHE_SIZE"         envDefault:"10000"`
	UserCacheTTL          time.Duration           `env:"USER_CACHE_TTL"          envDefault:"5m"`
	RoomMetaCacheSize     int                     `env:"ROOM_META_CACHE_SIZE"    envDefault:"10000"`
	RoomMetaCacheTTL      time.Duration           `env:"ROOM_META_CACHE_TTL"     envDefault:"2m"`
	ValkeyAddr            string                  `env:"VALKEY_ADDR"`
	ValkeyPassword        string                  `env:"VALKEY_PASSWORD"         envDefault:""`
	ValkeyKeyGracePeriod  time.Duration           `env:"VALKEY_KEY_GRACE_PERIOD" envDefault:"24h"`
	MetricsAddr           string                  `env:"METRICS_ADDR"            envDefault:":9090"`
	CacheStatsLogEnabled  bool                    `env:"CACHE_STATS_LOG_ENABLED" envDefault:"false"`
	CacheStatsLogInterval time.Duration           `env:"CACHE_STATS_LOG_INTERVAL" envDefault:"60s"`
	Consumer              stream.ConsumerSettings `envPrefix:"CONSUMER_"`
	Bootstrap             bootstrapConfig         `envPrefix:"BOOTSTRAP_"`
	Encryption            encryptionConfig        `envPrefix:"ENCRYPTION_"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	tracerShutdown, err := otelutil.InitTracer(ctx, "broadcast-worker")
	if err != nil {
		slog.Error("init tracer failed", "error", err)
		os.Exit(1)
	}

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}
	db := mongoClient.Database(cfg.MongoDB)
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
	stats := cachestats.New(prometheus.DefaultRegisterer)

	// Forward-declare the cache pointer so the sizeFn closure can reference it
	// before the cache instance exists. The closure is only called at scrape
	// time, after the pointer is assigned below.
	var metaStoreRef *cachedMetaStore
	metaRec := stats.Register("roommeta", func() int {
		if metaStoreRef == nil {
			return 0
		}
		return metaStoreRef.Len()
	})

	cachedStore, err := newCachedMetaStore(store, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL, metaRec)
	if err != nil {
		slog.Error("init room meta cache failed", "error", err)
		os.Exit(1)
	}
	metaStoreRef = cachedStore
	slog.Info("room-meta-cache enabled", "size", cfg.RoomMetaCacheSize, "ttl", cfg.RoomMetaCacheTTL)

	us := userstore.NewMongoStore(db.Collection("users"))
	if cfg.UserCacheSize > 0 && cfg.UserCacheTTL > 0 {
		// Only register the user-cache recorder when the cache is actually
		// enabled — no recorder is created for the disabled code path.
		var userStoreRef *CachedUserStore
		userRec := stats.Register("user", func() int {
			if userStoreRef == nil {
				return 0
			}
			return userStoreRef.Len()
		})
		cached := NewCachedUserStore(us, cfg.UserCacheSize, cfg.UserCacheTTL, userRec)
		userStoreRef = cached
		us = cached
		slog.Info("user-cache enabled", "size", cfg.UserCacheSize, "ttl", cfg.UserCacheTTL)
	} else {
		slog.Info("user-cache disabled")
	}

	var stopCacheLogger func()
	if cfg.CacheStatsLogEnabled {
		stopCacheLogger = stats.StartLogger(ctx, cfg.CacheStatsLogInterval, slog.Default())
		slog.Info("cache stats logger enabled", "interval", cfg.CacheStatsLogInterval)
	} else {
		stopCacheLogger = func() {}
	}

	var keyStore roomkeystore.RoomKeyStore
	if cfg.Encryption.Enabled {
		if cfg.ValkeyAddr == "" || cfg.ValkeyKeyGracePeriod <= 0 {
			slog.Error("encryption enabled but VALKEY_ADDR is empty or VALKEY_KEY_GRACE_PERIOD is not a positive duration",
				"valkey_addr_set", cfg.ValkeyAddr != "",
				"valkey_key_grace_period", cfg.ValkeyKeyGracePeriod)
			os.Exit(1)
		}
		keyStore, err = roomkeystore.NewValkeyStore(roomkeystore.Config{
			Addr:        cfg.ValkeyAddr,
			Password:    cfg.ValkeyPassword,
			GracePeriod: cfg.ValkeyKeyGracePeriod,
		})
		if err != nil {
			slog.Error("valkey connect failed", "error", err)
			os.Exit(1)
		}
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

	if err := bootstrapStreams(ctx, js, cfg.SiteID, cfg.Bootstrap.Enabled); err != nil {
		slog.Error("bootstrap streams failed", "error", err)
		os.Exit(1)
	}

	canonicalCfg := stream.MessagesCanonical(cfg.SiteID)

	cons, err := js.CreateOrUpdateConsumer(ctx, canonicalCfg.Name, buildConsumerConfig(cfg.Consumer))
	if err != nil {
		slog.Error("create consumer failed", "error", err)
		os.Exit(1)
	}

	publisher := &natsPublisher{nc: nc}
	handler := NewHandler(cachedStore, us, publisher, keyStore, cfg.Encryption.Enabled)

	// /metrics-only listener. All four timeouts guard against hung
	// scrapers tying up a goroutine indefinitely on an operator-exposed
	// port.
	//
	// Bind synchronously so a port conflict fails startup loudly —
	// otherwise ListenAndServe's error would surface in a goroutine and
	// the service would run happily with no /metrics, silently losing
	// observability. Serve(listener) takes ownership of the listener
	// from here on; Shutdown() closes it.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	metricsListener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		slog.Error("metrics server listen failed", "addr", cfg.MetricsAddr, "error", err)
		os.Exit(1)
	}
	go func() {
		slog.Info("metrics server listening", "addr", cfg.MetricsAddr)
		if err := metricsServer.Serve(metricsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server failed", "error", err)
		}
	}()

	iter, err := cons.Messages(jetstream.PullMaxMessages(2 * cfg.MaxWorkers))
	if err != nil {
		slog.Error("messages failed", "error", err)
		os.Exit(1)
	}

	sem := make(chan struct{}, cfg.MaxWorkers)
	var wg sync.WaitGroup

	go func() {
		for {
			msgCtx, msg, err := iter.Next()
			if err != nil {
				return
			}
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() {
					<-sem
					wg.Done()
				}()
				handlerCtx := natsutil.ContextWithRequestIDFromHeaders(msgCtx, msg.Headers())
				if err := handler.HandleMessage(handlerCtx, msg.Data()); err != nil {
					slog.Error("handle message failed", "error", err, "request_id", natsutil.RequestIDFromContext(handlerCtx))
					if err := msg.Nak(); err != nil {
						slog.Error("failed to nak message", "error", err)
					}
					return
				}
				if err := msg.Ack(); err != nil {
					slog.Error("failed to ack message", "error", err)
				}
			}()
		}
	}()

	slog.Info("broadcast-worker started", "site", cfg.SiteID, "encryption", cfg.Encryption.Enabled)

	hooks := []func(context.Context) error{
		// Stop the cache stats logger before draining so any final summary
		// line is emitted while the logger is still active.
		func(ctx context.Context) error { stopCacheLogger(); return nil },
		func(ctx context.Context) error {
			iter.Stop()
			return nil
		},
		func(ctx context.Context) error {
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("worker drain timed out: %w", ctx.Err())
			}
		},
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(ctx context.Context) error { return nc.Drain() },
	}
	if keyStore != nil {
		hooks = append(hooks, func(ctx context.Context) error { return keyStore.Close() })
	}
	// /metrics last so Prometheus can scrape the final drain-window observations.
	hooks = append(hooks,
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		func(ctx context.Context) error { return metricsServer.Shutdown(ctx) },
	)

	shutdown.Wait(ctx, 25*time.Second, hooks...)
}

// natsPublisher adapts *otelnats.Conn to the Publisher interface.
type natsPublisher struct {
	nc *otelnats.Conn
}

func (p *natsPublisher) Publish(ctx context.Context, subject string, data []byte) error {
	if err := p.nc.PublishMsg(ctx, natsutil.NewMsg(ctx, subject, data)); err != nil {
		return fmt.Errorf("publish to %q: %w", subject, err)
	}
	return nil
}

// buildConsumerConfig returns the durable consumer config for
// broadcast-worker. Centralized so it is unit-testable without NATS.
func buildConsumerConfig(s stream.ConsumerSettings) jetstream.ConsumerConfig {
	cc := stream.DurableConsumerDefaults(s)
	cc.Durable = "broadcast-worker"
	return cc
}
