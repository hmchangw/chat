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
	"github.com/hmchangw/chat/pkg/stream"
)

// bootstrapConfig groups every field that is ONLY meaningful when the worker
// is being stood up in dev or integration tests without its normal upstream
// services. In production none of these fields should be set — streams are
// owned by their publisher services (message-gatekeeper for
// MESSAGES_CANONICAL, inbox-worker for INBOX) and search-sync-worker only
// manages its own durable consumers.
//
// Env vars in this group are all prefixed `BOOTSTRAP_` so they're easy to
// spot in deployment manifests and obvious to grep.
type bootstrapConfig struct {
	// Enabled (BOOTSTRAP_STREAMS) toggles whether the worker calls
	// CreateOrUpdateStream at startup for each collection's stream. Leave
	// false in production.
	Enabled bool `env:"STREAMS" envDefault:"false"`
	// RemoteSiteIDs (BOOTSTRAP_REMOTE_SITE_IDS) lists the other sites whose
	// OUTBOX streams should be sourced into this site's INBOX when the
	// worker is creating it itself. Used to build the cross-site Sources +
	// SubjectTransforms config during bootstrap. Only consulted when
	// Enabled is true; unused in production.
	RemoteSiteIDs []string `env:"REMOTE_SITE_IDS" envSeparator:","`
}

type config struct {
	NatsURL        string          `env:"NATS_URL,required"`
	NatsCredsFile  string          `env:"NATS_CREDS_FILE" envDefault:""`
	SiteID         string          `env:"SITE_ID,required"`
	SearchURL      string          `env:"SEARCH_URL,required"`
	SearchBackend  string          `env:"SEARCH_BACKEND"  envDefault:"elasticsearch"`
	MsgIndexPrefix string          `env:"MSG_INDEX_PREFIX,required"`
	SpotlightIndex string          `env:"SPOTLIGHT_INDEX" envDefault:""`
	UserRoomIndex  string          `env:"USER_ROOM_INDEX" envDefault:""`
	BatchSize      int             `env:"BATCH_SIZE"      envDefault:"500"`
	FlushInterval  int             `env:"FLUSH_INTERVAL"  envDefault:"5"`
	Bootstrap      bootstrapConfig `envPrefix:"BOOTSTRAP_"`
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

	flushInterval := time.Duration(cfg.FlushInterval) * time.Second
	stopCh := make(chan struct{})
	doneChs := make([]chan struct{}, 0, len(collections))

	// Multiple collections can share the same stream (spotlight + user-room
	// both consume INBOX). Track which streams have already been created so
	// we don't redundantly call CreateOrUpdateStream per collection.
	createdStreams := make(map[string]struct{}, len(collections))

	// Canonical INBOX stream name, used below to decide when to layer on
	// cross-site Sources + SubjectTransforms during bootstrap.
	inboxName := stream.Inbox(cfg.SiteID).Name

	for _, coll := range collections {
		streamCfg := coll.StreamConfig(cfg.SiteID)
		if cfg.Bootstrap.Enabled {
			bootstrapCfg := streamCfg
			// The INBOX stream is the only one that needs cross-site Sources
			// + SubjectTransforms. Collections return a minimal baseline
			// (name + local subjects from pkg/stream.Inbox) and the
			// bootstrap path layers on the federation config here, keeping
			// the cross-site topology out of the Collection type entirely.
			if streamCfg.Name == inboxName {
				bootstrapCfg = inboxBootstrapStreamConfig(cfg.SiteID, cfg.Bootstrap.RemoteSiteIDs)
			}
			if _, alreadyCreated := createdStreams[bootstrapCfg.Name]; !alreadyCreated {
				if _, err := js.CreateOrUpdateStream(ctx, bootstrapCfg); err != nil {
					slog.Error("create stream failed", "stream", bootstrapCfg.Name, "error", err)
					os.Exit(1)
				}
				createdStreams[bootstrapCfg.Name] = struct{}{}
				slog.Info("stream bootstrapped", "stream", bootstrapCfg.Name)
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

		handler := NewHandler(&engineAdapter{engine: engine}, coll, cfg.BatchSize)
		doneCh := make(chan struct{})
		doneChs = append(doneChs, doneCh)

		slog.Info("collection wired",
			"stream", streamCfg.Name,
			"consumer", coll.ConsumerName(),
			"filters", consumerCfg.FilterSubjects,
		)

		go runConsumer(ctx, cons, handler, cfg.BatchSize, flushInterval, stopCh, doneCh)
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
