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
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"

	"github.com/hmchangw/chat/pkg/cachestats"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
)

type config struct {
	NatsURL               string                  `env:"NATS_URL,required"`
	NatsCredsFile         string                  `env:"NATS_CREDS_FILE" envDefault:""`
	SiteID                string                  `env:"SITE_ID,required"`
	MongoURI              string                  `env:"MONGO_URI,required"`
	MongoDB               string                  `env:"MONGO_DB"        envDefault:"chat"`
	MongoUsername         string                  `env:"MONGO_USERNAME"  envDefault:""`
	MongoPassword         string                  `env:"MONGO_PASSWORD"  envDefault:""`
	MaxWorkers            int                     `env:"MAX_WORKERS"     envDefault:"100"`
	LargeRoomThreshold    int                     `env:"LARGE_ROOM_THRESHOLD" envDefault:"500"`
	ChatBaseURL           string                  `env:"CHAT_BASE_URL"   envDefault:"http://localhost:3000"`
	SubCacheSize          int                     `env:"GATEKEEPER_SUB_CACHE_SIZE"  envDefault:"100000"`
	SubCacheTTL           time.Duration           `env:"GATEKEEPER_SUB_CACHE_TTL"   envDefault:"2m"`
	RoomMetaCacheSize     int                     `env:"ROOM_META_CACHE_SIZE"       envDefault:"10000"`
	RoomMetaCacheTTL      time.Duration           `env:"ROOM_META_CACHE_TTL"        envDefault:"2m"`
	MetricsAddr           string                  `env:"METRICS_ADDR"                envDefault:":9090"`
	CacheStatsLogEnabled  bool                    `env:"CACHE_STATS_LOG_ENABLED"     envDefault:"false"`
	CacheStatsLogInterval time.Duration           `env:"CACHE_STATS_LOG_INTERVAL"    envDefault:"60s"`
	Consumer              stream.ConsumerSettings `envPrefix:"CONSUMER_"`
	Bootstrap             bootstrapConfig         `envPrefix:"BOOTSTRAP_"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	tracerShutdown, err := otelutil.InitTracer(ctx, "message-gatekeeper")
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

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}
	db := mongoClient.Database(cfg.MongoDB)

	mongoStore := NewMongoStore(db)

	stats := cachestats.New(prometheus.DefaultRegisterer)

	// Forward declarations so sizeFn closures can reference the caches
	// before the cache instances exist. The closures are only called at
	// scrape time, after the caches are assigned below.
	var subStoreRef *cachedSubStore
	var metaStoreRef *cachedMetaStore

	subRec := stats.Register("subscription", func() int {
		if subStoreRef == nil {
			return 0
		}
		return subStoreRef.Len()
	})
	metaRec := stats.Register("roommeta", func() int {
		if metaStoreRef == nil {
			return 0
		}
		return metaStoreRef.Len()
	})

	withMeta, err := newCachedMetaStore(mongoStore, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL, metaRec)
	if err != nil {
		slog.Error("init room meta cache failed", "error", err)
		os.Exit(1)
	}
	metaStoreRef = withMeta

	store, err := newCachedSubStore(withMeta, cfg.SubCacheSize, cfg.SubCacheTTL, subRec)
	if err != nil {
		slog.Error("init subscription cache failed", "error", err)
		os.Exit(1)
	}
	subStoreRef = store

	slog.Info("gatekeeper caches enabled",
		"sub_cache_size", cfg.SubCacheSize, "sub_cache_ttl", cfg.SubCacheTTL,
		"room_meta_cache_size", cfg.RoomMetaCacheSize, "room_meta_cache_ttl", cfg.RoomMetaCacheTTL,
	)

	var stopCacheLogger func()
	if cfg.CacheStatsLogEnabled {
		stopCacheLogger = stats.StartLogger(ctx, cfg.CacheStatsLogInterval, slog.Default())
		slog.Info("cache stats logger enabled", "interval", cfg.CacheStatsLogInterval)
	} else {
		stopCacheLogger = func() {}
	}
	pub := func(ctx context.Context, msg *nats.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
		ack, err := js.PublishMsg(ctx, msg, opts...)
		if err != nil {
			return nil, fmt.Errorf("publish to %q: %w", msg.Subject, err)
		}
		return ack, nil
	}
	reply := func(ctx context.Context, msg *nats.Msg) error {
		if err := nc.PublishMsg(ctx, msg); err != nil {
			return fmt.Errorf("reply to %q: %w", msg.Subject, err)
		}
		return nil
	}
	parentFetcher := newHistoryParentFetcher(nc, cfg.ChatBaseURL)
	handler := NewHandler(store, pub, reply, cfg.SiteID, parentFetcher, cfg.LargeRoomThreshold)

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

	if err := bootstrapStreams(ctx, js, cfg.SiteID, cfg.Bootstrap.Enabled); err != nil {
		slog.Error("bootstrap streams failed", "error", err)
		os.Exit(1)
	}

	messagesCfg := stream.Messages(cfg.SiteID)
	cons, err := js.CreateOrUpdateConsumer(ctx, messagesCfg.Name, buildConsumerConfig(cfg.Consumer))
	if err != nil {
		slog.Error("create consumer failed", "error", err)
		os.Exit(1)
	}

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
				handler.HandleJetStreamMsg(handlerCtx, msg)
			}()
		}
	}()

	slog.Info("message-gatekeeper running", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
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
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		// /metrics last so Prometheus can scrape the final drain-window observations.
		func(ctx context.Context) error { return metricsServer.Shutdown(ctx) },
	)
}

// buildConsumerConfig returns the durable consumer config for
// message-gatekeeper. Centralized so it is unit-testable without NATS.
func buildConsumerConfig(s stream.ConsumerSettings) jetstream.ConsumerConfig {
	cc := stream.DurableConsumerDefaults(s)
	cc.Durable = "message-gatekeeper"
	return cc
}
