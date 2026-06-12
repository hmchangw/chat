package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"
	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/shutdown"
)

func main() {
	cfg, err := parseConfig()
	if err != nil {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)})))

	ctx := context.Background()

	tracerShutdown, err := otelutil.InitTracer(ctx, "oplog-connector")
	if err != nil {
		slog.Error("init tracer failed", "error", err)
		os.Exit(1)
	}

	conn, err := start(ctx, &cfg)
	if err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
	slog.Info("oplog-connector started", "site", cfg.SiteID, "collections", cfg.WatchCollections)

	// A fatal watcher error (e.g. lost resume token) exits non-zero without
	// waiting for a shutdown signal — recovery is operator-driven (reseed). The
	// goroutine also terminates on graceful shutdown via Done(), so it never
	// leaks. (No consumer-style worker has this path; it is unique to the pump.)
	go func() {
		select {
		case err := <-conn.Fatal():
			if err != nil {
				slog.Error("fatal watcher error — exiting", "error", err)
				_ = tracerShutdown(context.Background())
				conn.Close()
				os.Exit(1)
			}
		case <-conn.Done():
		}
	}()

	// Same shape as the other services: ordered, timeout-bounded cleanup steps.
	// stop readers → drain watchers → tracer → NATS → Mongo.
	shutdown.Wait(ctx, 25*time.Second,
		func(context.Context) error { conn.beginShutdown(); return nil },
		func(ctx context.Context) error { return conn.awaitWatchers(ctx) },
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(context.Context) error { return conn.nc.Drain() },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, conn.client); return nil },
	)
}

// connector owns the running watchers and the connections they share. Close
// stops all watchers (persisting their final checkpoints), then drains NATS and
// disconnects Mongo — in that order, per spec §7.3.
type connector struct {
	client *mongo.Client
	nc     *otelnats.Conn
	cancel context.CancelFunc
	wg     sync.WaitGroup
	fatal  chan error
	done   chan struct{}
	once   sync.Once
}

// start connects to the source Mongo and NATS, bootstraps the stream, and
// launches one watcher goroutine per watched collection. It returns a running
// connector; the caller drives lifecycle via Fatal() and Close().
func start(ctx context.Context, cfg *config) (*connector, error) {
	if cfg.StartResumeToken != "" || cfg.StartAtTime != "" {
		// These are one-off seed overrides. Left set in the environment, they
		// force a reseed (ignoring the stored checkpoint) on EVERY restart — so
		// surface it loudly. Prefer seeding via a pre-inserted checkpoint doc.
		slog.Warn("START_RESUME_TOKEN/START_AT_TIME is set — ignoring any stored checkpoint and reseeding; unset after first start to resume from the checkpoint",
			"startResumeTokenSet", cfg.StartResumeToken != "", "startAtTime", cfg.StartAtTime)
	}

	client, err := mongoutil.Connect(ctx, cfg.SourceMongoURI, cfg.SourceUsername, cfg.SourcePassword)
	if err != nil {
		return nil, fmt.Errorf("source mongo connect: %w", err)
	}

	nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
	if err != nil {
		mongoutil.Disconnect(ctx, client)
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := oteljetstream.New(nc)
	if err != nil {
		_ = nc.Drain()
		mongoutil.Disconnect(ctx, client)
		return nil, fmt.Errorf("jetstream init: %w", err)
	}
	if err := bootstrapStreams(ctx, js, cfg.SiteID, cfg.Bootstrap.Enabled); err != nil {
		_ = nc.Drain()
		mongoutil.Disconnect(ctx, client)
		return nil, fmt.Errorf("bootstrap streams: %w", err)
	}

	rp, err := readPreference(cfg.ReadPreference)
	if err != nil {
		_ = nc.Drain()
		mongoutil.Disconnect(ctx, client)
		return nil, err
	}

	store := NewMongoCheckpointStore(client.Database(cfg.CheckpointDB).Collection(checkpointCollection), cfg.SiteID)
	preimage := toSet(cfg.PreimageCollections)
	sourceDB := client.Database(cfg.SourceDB)

	watchCtx, cancel := context.WithCancel(context.Background())
	c := &connector{
		client: client,
		nc:     nc,
		cancel: cancel,
		fatal:  make(chan error, len(cfg.WatchCollections)),
		done:   make(chan struct{}),
	}
	checkpointMaxAge := time.Duration(cfg.CheckpointMaxAgeSeconds) * time.Second

	for _, raw := range cfg.WatchCollections {
		coll := strings.TrimSpace(raw)
		if coll == "" {
			continue
		}
		cp, err := store.Load(ctx, coll)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("load checkpoint %q: %w", coll, err)
		}
		sp, err := resolveStartPoint(cfg, cp)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("resolve start point %q: %w", coll, err)
		}
		mongoColl := sourceDB.Collection(coll,
			options.Collection().SetReadPreference(rp).SetReadConcern(readconcern.Majority()))
		src, err := openMongoChangeSource(watchCtx, mongoColl, sp, preimage[coll])
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("open change stream %q: %w", coll, err)
		}

		w := newWatcher(cfg.SiteID, coll, src, js, store, cfg.CheckpointEvery, checkpointMaxAge)
		c.wg.Add(1)
		go func(w *watcher) {
			defer c.wg.Done()
			if err := w.run(watchCtx); err != nil {
				c.fatal <- err
				cancel() // one fatal watcher tears the whole connector down
			}
		}(w)
	}

	return c, nil
}

// Fatal delivers the first fatal watcher error, if any.
func (c *connector) Fatal() <-chan error { return c.fatal }

// Done is closed when the connector shuts down, so a watcher of Fatal() can
// terminate on graceful shutdown instead of blocking forever.
func (c *connector) Done() <-chan struct{} { return c.done }

// beginShutdown signals every watcher to stop (idempotent). Each persists its
// final checkpoint as it exits.
func (c *connector) beginShutdown() {
	c.once.Do(func() {
		close(c.done)
		c.cancel()
	})
}

// awaitWatchers blocks until every watcher has exited (and flushed its final
// checkpoint), or ctx is done.
func (c *connector) awaitWatchers(ctx context.Context) error {
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("watcher drain timed out: %w", ctx.Err())
	}
}

// Close runs the full teardown in order — used by the fatal-exit path and by
// integration tests. main()'s signal path runs the same steps individually via
// shutdown.Wait so the sequence matches the other services.
func (c *connector) Close() {
	c.beginShutdown()
	wctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.awaitWatchers(wctx); err != nil {
		slog.Warn("watcher drain incomplete", "error", err)
	}
	_ = c.nc.Drain()
	mongoutil.Disconnect(context.Background(), c.client)
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[strings.TrimSpace(it)] = true
	}
	return m
}

func readPreference(s string) (*readpref.ReadPref, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "primary":
		return readpref.Primary(), nil
	case "primarypreferred":
		return readpref.PrimaryPreferred(), nil
	case "secondary", "":
		return readpref.Secondary(), nil
	case "secondarypreferred":
		return readpref.SecondaryPreferred(), nil
	case "nearest":
		return readpref.Nearest(), nil
	default:
		return nil, fmt.Errorf("invalid READ_PREFERENCE: %s", s)
	}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
