package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	dto "github.com/prometheus/client_model/go"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

type config struct {
	NatsURL       string `env:"NATS_URL,required"`
	NatsCredsFile string `env:"NATS_CREDS_FILE" envDefault:""`
	SiteID        string `env:"SITE_ID"         envDefault:"site-local"`
	MongoURI      string `env:"MONGO_URI,required"`
	MongoDB       string `env:"MONGO_DB"        envDefault:"chat"`
	MongoUsername string `env:"MONGO_USERNAME"  envDefault:""`
	MongoPassword string `env:"MONGO_PASSWORD"  envDefault:""`
	MetricsAddr   string `env:"METRICS_ADDR"    envDefault:":9099"`
	MaxInFlight   int    `env:"MAX_IN_FLIGHT"   envDefault:"200"`
	PProfAddr     string `env:"PPROF_ADDR"      envDefault:""`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: loadgen <seed|run|teardown> [flags]")
		os.Exit(2)
	}
	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}
	// SIGINT / SIGTERM cancel the base context. Each subcommand treats ctx
	// cancellation as "stop early but still run the end-of-run finalizers
	// (print summary, drain NATS, disconnect Mongo)".
	//
	// This deviates from CLAUDE.md's "use pkg/shutdown.Wait" guidance: that
	// helper blocks waiting for a signal and fires shutdown callbacks, which
	// doesn't fit a time-bounded CLI where the primary termination trigger is
	// the --duration timeout rather than an external signal. NotifyContext
	// gives us the same cleanup guarantee via context cancellation propagation.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := dispatch(ctx, &cfg)
	stop()
	os.Exit(code)
}

func dispatch(ctx context.Context, cfg *config) int {
	switch os.Args[1] {
	case "seed":
		return runSeed(ctx, cfg, os.Args[2:])
	case "run":
		return runRun(ctx, cfg, os.Args[2:])
	case "teardown":
		return runTeardown(ctx, cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		return 2
	}
}

func runSeed(ctx context.Context, cfg *config, args []string) int {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	preset := fs.String("preset", "", "preset name")
	seed := fs.Int64("seed", 42, "RNG seed")
	_ = fs.Parse(args)
	if *preset == "" {
		fmt.Fprintln(os.Stderr, "--preset required")
		return 2
	}
	p, ok := BuiltinPreset(*preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown preset: %s\n", *preset)
		return 2
	}
	client, err := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if err != nil {
		slog.Error("mongo connect", "error", err)
		return 1
	}
	defer mongoutil.Disconnect(ctx, client)
	db := client.Database(cfg.MongoDB)
	fixtures := BuildFixtures(&p, *seed, cfg.SiteID)
	if err := Seed(ctx, db, fixtures); err != nil {
		slog.Error("seed", "error", err)
		return 1
	}
	slog.Info("seed complete",
		"preset", p.Name,
		"users", len(fixtures.Users),
		"rooms", len(fixtures.Rooms),
		"subs", len(fixtures.Subscriptions))
	return 0
}

func runTeardown(ctx context.Context, cfg *config) int {
	client, err := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if err != nil {
		slog.Error("mongo connect", "error", err)
		return 1
	}
	defer mongoutil.Disconnect(ctx, client)
	db := client.Database(cfg.MongoDB)
	if err := Teardown(ctx, db); err != nil {
		slog.Error("teardown", "error", err)
		return 1
	}
	slog.Info("teardown complete")
	return 0
}

func runRun(ctx context.Context, cfg *config, args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	preset := fs.String("preset", "", "preset name")
	seed := fs.Int64("seed", 42, "RNG seed")
	duration := fs.Duration("duration", 60*time.Second, "run duration")
	rate := fs.Int("rate", 500, "target msgs/sec")
	warmup := fs.Duration("warmup", 10*time.Second, "warmup window (samples discarded)")
	inject := fs.String("inject", "frontdoor", "injection point: frontdoor|canonical")
	csvPath := fs.String("csv", "", "optional csv output path")
	_ = fs.Parse(args)
	if *preset == "" {
		fmt.Fprintln(os.Stderr, "--preset required")
		return 2
	}
	p, ok := BuiltinPreset(*preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown preset: %s\n", *preset)
		return 2
	}
	var injectMode InjectMode
	switch *inject {
	case "frontdoor":
		injectMode = InjectFrontdoor
	case "canonical":
		injectMode = InjectCanonical
	default:
		fmt.Fprintf(os.Stderr, "unknown inject mode: %s\n", *inject)
		return 2
	}

	nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
	if err != nil {
		slog.Error("nats connect", "error", err)
		return 1
	}
	js, err := jetstream.New(nc.NatsConn())
	if err != nil {
		slog.Error("jetstream init", "error", err)
		return 1
	}

	metrics := NewMetrics()
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metrics.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("metrics server stopped", "error", err)
		}
	}()

	// pprof lives on a separate port, opt-in via PPROF_ADDR. Off by default
	// so the metrics endpoint (which Prometheus scrapes) doesn't
	// inadvertently expose profiling. Handlers are registered on a dedicated
	// mux rather than http.DefaultServeMux to avoid leaking debug endpoints
	// onto any other server that happens to use the default mux.
	var pprofSrv *http.Server
	if cfg.PProfAddr != "" {
		pprofMux := http.NewServeMux()
		pprofMux.HandleFunc("/debug/pprof/", pprof.Index)
		pprofMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		pprofMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		pprofMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		pprofMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		pprofSrv = &http.Server{
			Addr:              cfg.PProfAddr,
			Handler:           pprofMux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			if err := pprofSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Warn("pprof server stopped", "error", err)
			}
		}()
		slog.Info("pprof server listening", "addr", cfg.PProfAddr)
	}

	fixtures := BuildFixtures(&p, *seed, cfg.SiteID)
	collector := NewCollector(metrics, p.Name)

	// E1 subscription: gatekeeper replies.
	e1Sub, err := nc.NatsConn().Subscribe(subject.UserResponseWildcard(), func(msg *nats.Msg) {
		reqID := lastToken(msg.Subject)
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			// Malformed reply; count and drop per spec.
			metrics.PublishErrors.WithLabelValues(p.Name, "bad_reply").Inc()
			return
		}
		if payload.Error != "" {
			metrics.PublishErrors.WithLabelValues(p.Name, "gatekeeper").Inc()
		}
		collector.RecordReply(reqID, time.Now())
	})
	if err != nil {
		slog.Error("subscribe e1", "error", err)
		return 1
	}
	defer func() { _ = e1Sub.Unsubscribe() }()

	// E2 subscription: broadcast events.
	e2Handler := func(msg *nats.Msg) {
		var evt model.RoomEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		if evt.Message == nil || evt.Message.ID == "" {
			return
		}
		collector.RecordBroadcast(evt.Message.ID, time.Now())
	}

	e2Sub, err := nc.NatsConn().Subscribe(subject.RoomEventWildcard(), e2Handler)
	if err != nil {
		slog.Error("subscribe e2", "error", err)
		return 1
	}
	defer func() { _ = e2Sub.Unsubscribe() }()

	// Broadcast-worker emits DM broadcasts on chat.user.{account}.event.room
	// (see pkg/subject.UserRoomEvent). Subscribe to both so E2 correlation
	// covers both group and DM rooms.
	e2DMSub, err := nc.NatsConn().Subscribe(subject.UserRoomEventWildcard(), e2Handler)
	if err != nil {
		slog.Error("subscribe e2 dm", "error", err)
		return 1
	}
	defer func() { _ = e2DMSub.Unsubscribe() }()

	canonical := stream.MessagesCanonical(cfg.SiteID)
	samplerCtx, cancelSamplers := context.WithCancel(ctx)
	defer cancelSamplers()
	samplers := []*ConsumerSampler{
		NewConsumerSampler(js, canonical.Name, "message-worker", metrics, 1*time.Second),
		NewConsumerSampler(js, canonical.Name, "broadcast-worker", metrics, 1*time.Second),
	}
	var samplerWG sync.WaitGroup
	for _, s := range samplers {
		samplerWG.Add(1)
		go func(s *ConsumerSampler) {
			defer samplerWG.Done()
			s.Run(samplerCtx)
		}(s)
	}

	publisher := newNatsCorePublisher(nc.NatsConn(), injectMode, js)

	warmupDeadline := time.Now().Add(*warmup)
	gen := NewGenerator(&GeneratorConfig{
		Preset:         &p,
		Fixtures:       fixtures,
		SiteID:         cfg.SiteID,
		Rate:           *rate,
		Inject:         injectMode,
		Publisher:      publisher,
		Metrics:        metrics,
		Collector:      collector,
		WarmupDeadline: warmupDeadline,
		MaxInFlight:    cfg.MaxInFlight,
	}, *seed)

	runCtx, cancelRun := context.WithTimeout(ctx, *duration)
	defer cancelRun()
	genErr := gen.Run(runCtx)
	// Wait up to 2 seconds for trailing replies and broadcasts to arrive.
	time.Sleep(2 * time.Second)
	collector.DiscardBefore(warmupDeadline)
	missingReplies, missingBroadcasts := collector.Finalize()

	cancelSamplers()
	samplerWG.Wait()

	shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
	_ = metricsSrv.Shutdown(shutCtx)
	if pprofSrv != nil {
		_ = pprofSrv.Shutdown(shutCtx)
	}
	cancelShut()
	_ = nc.Drain()

	if genErr != nil {
		slog.Error("generator error", "error", genErr)
	}

	mfs, gerr := metrics.Registry.Gather()
	if gerr != nil {
		slog.Warn("metrics gather", "error", gerr)
		mfs = nil
	}
	publishErrs := gatheredCounterValue(mfs, "loadgen_publish_errors_total", "", "")
	gkErrs := gatheredCounterValue(mfs, "loadgen_publish_errors_total", "reason", "gatekeeper")
	sentWarmup := int(gatheredCounterValue(mfs, "loadgen_published_total", "phase", "warmup"))
	sentMeasured := int(gatheredCounterValue(mfs, "loadgen_published_total", "phase", "measured"))
	sent := sentWarmup + sentMeasured
	measured := *duration - *warmup
	actualRate := 0.0
	if measured > 0 {
		// In canonical mode, byReqID is never populated, so E1Count/missingReplies
		// are both 0. Fall back to sentMeasured to compute the true publish rate
		// for the measured window only.
		switch injectMode {
		case InjectCanonical:
			actualRate = float64(sentMeasured) / measured.Seconds()
		default:
			actualRate = float64(collector.E1Count()+missingReplies) / measured.Seconds()
		}
	}

	summary := Summary{
		Preset:            p.Name,
		Seed:              *seed,
		Site:              cfg.SiteID,
		TargetRate:        *rate,
		ActualRate:        actualRate,
		Duration:          *duration,
		Warmup:            *warmup,
		Inject:            *inject,
		Sent:              sent,
		SentMeasured:      sentMeasured,
		PublishErrors:     int(publishErrs - gkErrs),
		GatekeeperErrors:  int(gkErrs),
		MissingReplies:    missingReplies,
		MissingBroadcasts: missingBroadcasts,
		E1:                ComputePercentiles(collector.E1Samples()),
		E2:                ComputePercentiles(collector.E2Samples()),
		E1Count:           collector.E1Count(),
		E2Count:           collector.E2Count(),
		Consumers:         []ConsumerStat{samplers[0].Snapshot(), samplers[1].Snapshot()},
	}
	if err := PrintSummary(os.Stdout, &summary); err != nil {
		slog.Warn("print summary", "error", err)
	}

	if *csvPath != "" {
		if err := writeCSVFile(*csvPath, collector); err != nil {
			slog.Error("csv export", "error", err)
		}
	}

	totalErrs := summary.PublishErrors + summary.GatekeeperErrors + summary.MissingReplies + summary.MissingBroadcasts
	return DetermineExitCode(summary.SentMeasured, totalErrs)
}

type natsCorePublisher struct {
	nc           *nats.Conn
	useJetStream bool
	js           jetstream.JetStream
}

func newNatsCorePublisher(nc *nats.Conn, inject InjectMode, js jetstream.JetStream) *natsCorePublisher {
	return &natsCorePublisher{nc: nc, useJetStream: inject == InjectCanonical, js: js}
}

func (p *natsCorePublisher) Publish(ctx context.Context, subject string, data []byte) error {
	if p.useJetStream {
		if _, err := p.js.Publish(ctx, subject, data); err != nil {
			return fmt.Errorf("jetstream publish: %w", err)
		}
		return nil
	}
	if err := p.nc.Publish(subject, data); err != nil {
		return fmt.Errorf("core publish: %w", err)
	}
	return nil
}

func lastToken(subj string) string {
	i := strings.LastIndex(subj, ".")
	if i < 0 {
		return subj
	}
	return subj[i+1:]
}

func writeCSVFile(path string, c *Collector) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create csv: %w", err)
	}
	defer func() { _ = f.Close() }()
	var rows []CSVSample
	for i, d := range c.E1Samples() {
		rows = append(rows, CSVSample{TimestampNs: int64(i), Metric: "E1", LatencyNs: d.Nanoseconds()})
	}
	for i, d := range c.E2Samples() {
		rows = append(rows, CSVSample{TimestampNs: int64(i), Metric: "E2", LatencyNs: d.Nanoseconds()})
	}
	return WriteCSV(f, rows)
}

func gatheredCounterValue(mfs []*dto.MetricFamily, name string, labelName, labelValue string) float64 {
	var total float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			if labelName == "" {
				total += metric.GetCounter().GetValue()
				continue
			}
			for _, l := range metric.GetLabel() {
				if l.GetName() == labelName && l.GetValue() == labelValue {
					total += metric.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}

func counterValue(m *Metrics, name string) float64 {
	mfs, err := m.Registry.Gather()
	if err != nil {
		slog.Warn("metrics gather", "error", err)
		return 0
	}
	return gatheredCounterValue(mfs, name, "", "")
}

func counterValueLabeled(m *Metrics, name, labelName, labelValue string) float64 {
	mfs, err := m.Registry.Gather()
	if err != nil {
		slog.Warn("metrics gather", "error", err)
		return 0
	}
	return gatheredCounterValue(mfs, name, labelName, labelValue)
}
