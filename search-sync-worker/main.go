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

type config struct {
	NatsURL        string `env:"NATS_URL,required"`
	NatsCredsFile  string `env:"NATS_CREDS_FILE" envDefault:""`
	SiteID         string `env:"SITE_ID,required"`
	SearchURL      string `env:"SEARCH_URL,required"`
	SearchBackend  string `env:"SEARCH_BACKEND"  envDefault:"elasticsearch"`
	MsgIndexPrefix string `env:"MSG_INDEX_PREFIX,required"`
	BatchSize      int    `env:"BATCH_SIZE"      envDefault:"500"`
	FlushInterval  int    `env:"FLUSH_INTERVAL"  envDefault:"5"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
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

	coll := newMessageCollection(cfg.MsgIndexPrefix)

	tmplName := coll.TemplateName()
	tmplBody := coll.TemplateBody()
	if err := engine.UpsertTemplate(ctx, tmplName, tmplBody); err != nil {
		slog.Error("upsert index template failed", "error", err)
		os.Exit(1)
	}
	slog.Info("index template upserted", "name", tmplName)

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

	canonicalCfg := coll.StreamConfig(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     canonicalCfg.Name,
		Subjects: canonicalCfg.Subjects,
	}); err != nil {
		slog.Error("create MESSAGES_CANONICAL stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, canonicalCfg.Name, jetstream.ConsumerConfig{
		Durable:   coll.ConsumerName(),
		AckPolicy: jetstream.AckExplicitPolicy,
		BackOff:   []time.Duration{1 * time.Second, 5 * time.Second, 30 * time.Second},
	})
	if err != nil {
		slog.Error("create consumer failed", "error", err)
		os.Exit(1)
	}

	handler := NewHandler(&engineAdapter{engine: engine}, coll, cfg.BatchSize)

	flushInterval := time.Duration(cfg.FlushInterval) * time.Second
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	go runConsumer(ctx, cons, handler, cfg.BatchSize, flushInterval, stopCh, doneCh)

	slog.Info("search-sync-worker running", "site", cfg.SiteID, "prefix", cfg.MsgIndexPrefix)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error {
			close(stopCh)
			return nil
		},
		func(ctx context.Context) error {
			select {
			case <-doneCh:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("consumer loop drain timed out: %w", ctx.Err())
			}
		},
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(ctx context.Context) error { return nc.Drain() },
	)
}

// runConsumer is the batch-flush consumer loop for a single collection.
// It fetches messages from the JetStream consumer, adds them to the handler buffer,
// and flushes when the buffer is full or the flush interval elapses.
func runConsumer(ctx context.Context, cons oteljetstream.Consumer, handler *Handler, batchSize int, flushInterval time.Duration, stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)
	lastFlush := time.Now()

	for {
		select {
		case <-stopCh:
			handler.Flush(ctx)
			return
		default:
		}

		fetchSize := batchSize - handler.BufferLen()
		if fetchSize <= 0 {
			fetchSize = 1
		}

		batch, err := cons.Fetch(fetchSize, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			select {
			case <-stopCh:
				handler.Flush(ctx)
				return
			default:
			}
			if handler.BufferLen() > 0 && time.Since(lastFlush) >= flushInterval {
				handler.Flush(ctx)
				lastFlush = time.Now()
			}
			continue
		}

		for msg := range batch.Messages() {
			handler.Add(msg.Msg)
		}

		if handler.BufferFull() {
			handler.Flush(ctx)
			lastFlush = time.Now()
		} else if handler.BufferLen() > 0 && time.Since(lastFlush) >= flushInterval {
			handler.Flush(ctx)
			lastFlush = time.Now()
		}
	}
}

// engineAdapter adapts searchengine.SearchEngine to the Handler's Store interface.
type engineAdapter struct {
	engine searchengine.SearchEngine
}

func (a *engineAdapter) Bulk(ctx context.Context, actions []searchengine.BulkAction) ([]searchengine.BulkResult, error) {
	return a.engine.Bulk(ctx, actions)
}
