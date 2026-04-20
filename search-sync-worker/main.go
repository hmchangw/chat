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

	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/searchengine"
	"github.com/hmchangw/chat/pkg/shutdown"
)

// bootstrapConfig groups fields meaningful ONLY in dev / integration tests.
// In production, streams are owned by their publisher services and
// search-sync-worker only manages its own durable consumers.
type bootstrapConfig struct {
	Enabled bool `env:"STREAMS" envDefault:"false"`
}

type config struct {
	NatsURL           string          `env:"NATS_URL,required"`
	NatsCredsFile     string          `env:"NATS_CREDS_FILE" envDefault:""`
	SiteID            string          `env:"SITE_ID,required"`
	SearchURL         string          `env:"SEARCH_URL,required"`
	SearchBackend     string          `env:"SEARCH_BACKEND"    envDefault:"elasticsearch"`
	MsgIndexPrefix    string          `env:"MSG_INDEX_PREFIX,required"`
	SpotlightIndex    string          `env:"SPOTLIGHT_INDEX"   envDefault:""`
	UserRoomIndex     string          `env:"USER_ROOM_INDEX"   envDefault:""`
	FetchBatchSize    int             `env:"FETCH_BATCH_SIZE"  envDefault:"100"`
	BulkBatchSize     int             `env:"BULK_BATCH_SIZE"   envDefault:"500"`
	BulkFlushInterval int             `env:"BULK_FLUSH_INTERVAL" envDefault:"5"`
	Bootstrap         bootstrapConfig `envPrefix:"BOOTSTRAP_"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	if cfg.SpotlightIndex == "" {
		cfg.SpotlightIndex = fmt.Sprintf("spotlight-%s-v1-chat", cfg.SiteID)
	}
	if cfg.UserRoomIndex == "" {
		cfg.UserRoomIndex = fmt.Sprintf("user-room-%s", cfg.SiteID)
	}

	if cfg.FetchBatchSize <= 0 {
		slog.Error("invalid config", "name", "FETCH_BATCH_SIZE", "value", cfg.FetchBatchSize, "reason", "must be > 0")
		os.Exit(1)
	}
	if cfg.BulkBatchSize <= 0 {
		slog.Error("invalid config", "name", "BULK_BATCH_SIZE", "value", cfg.BulkBatchSize, "reason", "must be > 0")
		os.Exit(1)
	}
	if cfg.BulkFlushInterval <= 0 {
		slog.Error("invalid config", "name", "BULK_FLUSH_INTERVAL", "value", cfg.BulkFlushInterval, "reason", "must be > 0")
		os.Exit(1)
	}

	ctx := context.Background()

	tracerShutdown, err := otelutil.InitTracer(ctx, "search-sync-worker")
	if err != nil {
		slog.Error("init tracer failed", "error", err)
		os.Exit(1)
	}

	engine, err := searchengine.New(ctx, cfg.SearchBackend, cfg.SearchURL)
	if err != nil {
		slog.Error("search engine connect failed", "error", err)
		os.Exit(1)
	}

	collections := []Collection{
		newMessageCollection(cfg.MsgIndexPrefix),
		newSpotlightCollection(cfg.SpotlightIndex),
		newUserRoomCollection(cfg.UserRoomIndex),
	}

	for _, coll := range collections {
		name := coll.TemplateName()
		body := coll.TemplateBody()
		if name == "" || body == nil {
			continue
		}
		if err := engine.UpsertTemplate(ctx, name, body); err != nil {
			slog.Error("upsert index template failed", "template", name, "error", err)
			os.Exit(1)
		}
		slog.Info("index template upserted", "name", name)
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

	bulkFlushInterval := time.Duration(cfg.BulkFlushInterval) * time.Second
	stopCh := make(chan struct{})
	doneChs := make([]chan struct{}, 0, len(collections))

	createdStreams := make(map[string]struct{}, len(collections))

	for _, coll := range collections {
		streamCfg := coll.StreamConfig(cfg.SiteID)
		if cfg.Bootstrap.Enabled {
			if _, alreadyCreated := createdStreams[streamCfg.Name]; !alreadyCreated {
				if _, err := js.CreateOrUpdateStream(ctx, streamCfg); err != nil {
					slog.Error("create stream failed", "stream", streamCfg.Name, "error", err)
					os.Exit(1)
				}
				createdStreams[streamCfg.Name] = struct{}{}
				slog.Info("stream bootstrapped", "stream", streamCfg.Name)
			}
		}

		consumerCfg := jetstream.ConsumerConfig{
			Durable:   coll.ConsumerName(),
			AckPolicy: jetstream.AckExplicitPolicy,
			BackOff:   []time.Duration{1 * time.Second, 5 * time.Second, 30 * time.Second},
		}
		if filters := coll.FilterSubjects(cfg.SiteID); len(filters) > 0 {
			consumerCfg.FilterSubjects = filters
		}
		cons, err := js.CreateOrUpdateConsumer(ctx, streamCfg.Name, consumerCfg)
		if err != nil {
			slog.Error("create consumer failed",
				"stream", streamCfg.Name,
				"consumer", coll.ConsumerName(),
				"error", err,
			)
			os.Exit(1)
		}

		handler := NewHandler(&engineAdapter{engine: engine}, coll, cfg.BulkBatchSize)
		doneCh := make(chan struct{})
		doneChs = append(doneChs, doneCh)

		slog.Info("collection wired",
			"stream", streamCfg.Name,
			"consumer", coll.ConsumerName(),
			"filters", consumerCfg.FilterSubjects,
		)

		go runConsumer(ctx, cons, handler, cfg.FetchBatchSize, cfg.BulkBatchSize, bulkFlushInterval, stopCh, doneCh)
	}

	slog.Info("search-sync-worker running",
		"site", cfg.SiteID,
		"msgPrefix", cfg.MsgIndexPrefix,
		"spotlightIndex", cfg.SpotlightIndex,
		"userRoomIndex", cfg.UserRoomIndex,
		"collections", len(collections),
	)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error {
			close(stopCh)
			return nil
		},
		func(ctx context.Context) error {
			for _, ch := range doneChs {
				select {
				case <-ch:
				case <-ctx.Done():
					return fmt.Errorf("consumer loop drain timed out: %w", ctx.Err())
				}
			}
			return nil
		},
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(ctx context.Context) error { return nc.Drain() },
	)
}

func runConsumer(
	ctx context.Context,
	cons oteljetstream.Consumer,
	handler *Handler,
	fetchBatchSize, bulkBatchSize int,
	bulkFlushInterval time.Duration,
	stopCh <-chan struct{},
	doneCh chan<- struct{},
) {
	defer close(doneCh)
	lastFlush := time.Now()

	for {
		select {
		case <-stopCh:
			handler.Flush(ctx)
			return
		default:
		}

		remaining := bulkBatchSize - handler.ActionCount()
		if remaining <= 0 {
			handler.Flush(ctx)
			lastFlush = time.Now()
			continue
		}
		fetchCount := fetchBatchSize
		if fetchCount > remaining {
			fetchCount = remaining
		}

		batch, err := cons.Fetch(fetchCount, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			select {
			case <-stopCh:
				handler.Flush(ctx)
				return
			default:
			}
			if handler.ActionCount() > 0 && time.Since(lastFlush) >= bulkFlushInterval {
				handler.Flush(ctx)
				lastFlush = time.Now()
			}
			continue
		}

		for msg := range batch.Messages() {
			handler.Add(msg.Msg)
			if handler.ActionCount() >= bulkBatchSize {
				handler.Flush(ctx)
				lastFlush = time.Now()
			}
		}

		if handler.ActionCount() >= bulkBatchSize {
			handler.Flush(ctx)
			lastFlush = time.Now()
		} else if handler.ActionCount() > 0 && time.Since(lastFlush) >= bulkFlushInterval {
			handler.Flush(ctx)
			lastFlush = time.Now()
		}
	}
}

type engineAdapter struct {
	engine searchengine.SearchEngine
}

func (a *engineAdapter) Bulk(ctx context.Context, actions []searchengine.BulkAction) ([]searchengine.BulkResult, error) {
	return a.engine.Bulk(ctx, actions)
}
