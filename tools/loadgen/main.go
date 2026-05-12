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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	dto "github.com/prometheus/client_model/go"

	"github.com/hmchangw/chat/pkg/idgen"
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
	override := fs.Bool("i-know-what-i-am-doing", false,
		"override the MONGO_DB=loadgen* isolation guard; use ONLY for one-off recoveries")
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
	if err := guardMongoDB(cfg.MongoDB, *override); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
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
	// Teardown is destructive — refuse unless the configured DB carries
	// the loadgen prefix. Override flag is intentionally not exposed
	// for the teardown subcommand; an operator who genuinely needs to
	// drop a non-loadgen DB can rename it temporarily or use mongosh
	// directly.
	if err := guardMongoDB(cfg.MongoDB, false); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
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
	rf, err := ParseRunFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Print the canonical --help output to stderr and exit 0 so
			// `go run ./tools/loadgen run --help` does not emit "exit status N"
			// (go run only emits that suffix on non-zero exits).
			PrintRunHelp(os.Stderr)
			return 0
		}
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	if err := parseScenarioFlag(rf.Scenario); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	if rf.Preset == "" {
		fmt.Fprintln(os.Stderr, "--preset required")
		return 2
	}
	p, ok := BuiltinPreset(rf.Preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown preset: %s\n", rf.Preset)
		return 2
	}
	injectMode, err := parseInjectMode(rf.Inject)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}

	// Validate ramp + rate flags BEFORE opening any external connections —
	// fail fast on config errors so the operator doesn't wait for a NATS
	// timeout to learn they typo'd a flag.
	ramp, rerr := buildRamp(rf.Ramp.From, rf.Ramp.To, rf.Ramp.Duration, rf.Ramp.Shape)
	if rerr != nil {
		fmt.Fprintln(os.Stderr, rerr.Error())
		return 2
	}
	if err := validateRampVsRate(rf.Rate, ramp); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}

	nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
	if err != nil {
		slog.Error("nats connect", "error", err)
		return 1
	}
	metrics := NewMetrics()
	// Collector built BEFORE jetstream.New so the S5 async err handler
	// (constructed inside jetstreamPublishOpts) can capture it and evict
	// orphan messageIDs on async ack failures — without that eviction,
	// every async_ack would also inflate Finalize's MissingBroadcasts.
	collector := NewCollector(metrics, p.Name)
	// S5: when --js-async-max-pending>0 AND inject=canonical, jetstream.New
	// is built with an async-publish in-flight bound and an err-handler
	// that (a) bumps loadgen_publish_errors_total{reason="async_ack"} and
	// (b) calls collector.RecordPublishFailed("", messageID) on the
	// orphaned canonical event. Built BEFORE jetstream.New so the handler
	// is wired from the first publish. Frontdoor inject doesn't use the
	// async path so the opts are skipped to avoid pre-allocating an
	// unused ring buffer (R2 NIT #5).
	var jsOpts []jetstream.JetStreamOpt
	if injectMode == InjectCanonical {
		jsOpts = jetstreamPublishOpts(rf.JS.AsyncMaxPending, metrics, p.Name, collector)
	}
	js, err := jetstream.New(nc.NatsConn(), jsOpts...)
	if err != nil {
		slog.Error("jetstream init", "error", err)
		return 1
	}
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

	fixtures := BuildFixtures(&p, rf.Seed, cfg.SiteID)
	// Phase 3 §3.5: in-process latency ring buffer for the abort watcher.
	// Sized to retain max(--abort-p99-sustain, --abort-error-sustain, 60s).
	windowRetain := 60 * time.Second
	if rf.Abort.P99Sustain > windowRetain {
		windowRetain = rf.Abort.P99Sustain
	}
	if rf.Abort.ErrorSustain > windowRetain {
		windowRetain = rf.Abort.ErrorSustain
	}
	// S3: bound the sort cost at high publish rates. At 5k rps × 60s
	// retain the unbounded slice would hold 300k samples; cap default
	// 10k keeps each percentile sort under ~150µs. The cap can mask a
	// sustained breach when (peak_rps × max_sustain) > cap — see the
	// "abort watcher deafened by sample cap" slog.Warn in window.go.
	//
	// Bug 6: auto-size the cap when the user did not explicitly pass
	// --abort-window-max-samples so default flags ship in a non-deafened
	// state. Detected via ParseRunFlags (WindowMaxSamplesSet records whether
	// the flag was explicitly passed). Operator overrides (including 0 =
	// disabled) pass through verbatim.
	abortMaxSustain := rf.Abort.P99Sustain
	if rf.Abort.ErrorSustain > abortMaxSustain {
		abortMaxSustain = rf.Abort.ErrorSustain
	}
	resolvedAbortCap := resolveAbortWindowMaxSamples(
		rf.Abort.WindowMaxSamplesSet, rf.Abort.WindowMaxSamples,
		resolveAbortPeakRPS(rf.Rate, rf.Ramp.To), abortMaxSustain,
	)
	if resolvedAbortCap != rf.Abort.WindowMaxSamples {
		slog.Info("abort-window-max-samples auto-sized",
			"original", rf.Abort.WindowMaxSamples, "resolved", resolvedAbortCap,
			"peak_rps", resolveAbortPeakRPS(rf.Rate, rf.Ramp.To),
			"max_sustain", abortMaxSustain.String())
	}
	latencyWindow := NewLatencyWindow(windowRetain).WithMaxSamples(resolvedAbortCap)
	collector.AttachWindow(latencyWindow)

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
	rooms := stream.Rooms(cfg.SiteID)
	inbox := stream.Inbox(cfg.SiteID)
	samplerCtx, cancelSamplers := context.WithCancel(ctx)
	defer cancelSamplers()
	samplers := []*ConsumerSampler{
		// MESSAGES_CANONICAL consumers (driven by messaging-pipeline).
		NewConsumerSampler(js, canonical.Name, "message-worker", metrics, 1*time.Second),
		NewConsumerSampler(js, canonical.Name, "broadcast-worker", metrics, 1*time.Second),
		NewConsumerSampler(js, canonical.Name, "notification-worker", metrics, 1*time.Second),
		NewConsumerSampler(js, canonical.Name, "search-sync-worker-messages", metrics, 1*time.Second),
		// ROOMS consumer (driven by room-rpc).
		NewConsumerSampler(js, rooms.Name, "room-worker", metrics, 1*time.Second),
		// INBOX consumers — populated either by federation or by local-publish
		// from message-worker / room-worker. Empty gauges read 0 cleanly when
		// the relevant scenario isn't running.
		NewConsumerSampler(js, inbox.Name, "search-sync-worker-spotlight", metrics, 1*time.Second),
		NewConsumerSampler(js, inbox.Name, "search-sync-worker-user-room", metrics, 1*time.Second),
	}
	var samplerWG sync.WaitGroup
	for _, s := range samplers {
		samplerWG.Add(1)
		go func(s *ConsumerSampler) {
			defer samplerWG.Done()
			s.Run(samplerCtx)
		}(s)
	}

	// Phase 3 §3.6: ConnPool with the observer reused as the
	// reply / broadcast / sampler connection, plus optional N data
	// connections for per-user fan-out. --connections=1 falls back to
	// observer-only (today's behavior).
	//
	// C2 prep: when --nats-creds-dir is set, each data conn dials with
	// a different *.creds file from that dir. Full per-fixture-user
	// auth requires auth-service in the compose stack (deferred);
	// today this rotates whatever pre-minted creds the operator
	// supplies. Empty dir → fall back to cfg.NatsCredsFile for every
	// dial (existing behavior).
	credsFiles, credsErr := LoadCredsDir(rf.NATSCredsDir)
	if credsErr != nil {
		slog.Error("nats creds dir", "error", credsErr)
		return 1
	}
	if len(credsFiles) > 0 {
		// F4: only log "rotating" when rotation can actually happen.
		// With --connections<=1 the pool collapses to {observer} and the
		// credsFiles slice is silently ignored — that path used to
		// emit a misleading info-line claiming rotation was active.
		if rf.Conn.Connections > 1 {
			// F3: the user-to-creds binding isn't implemented yet; refuse
			// rather than silently mis-route per-user traffic. Liveness
			// probes still work (they use pool.Observer with global creds);
			// scenario traffic would get permission-denied once
			// auth-service is enforcing. Operators wanting auth realism
			// today should use --connections=1 + a single creds file via
			// NATS_CREDS_FILE.
			fmt.Fprintln(os.Stderr,
				"--nats-creds-dir + --connections>1 is unsafe today: per-user→creds binding "+
					"is not yet implemented (deferred until full C2 lands). Use --connections=1 with "+
					"NATS_CREDS_FILE for shared-creds runs, or wait for the user-binding follow-up.")
			return 2
		}
		slog.Warn("--nats-creds-dir ignored: --connections=1 routes all traffic through the observer (cfg.NatsCredsFile)",
			"creds_count", len(credsFiles))
	}
	pool, perr := NewConnPoolWithCreds(
		nc.NatsConn(), cfg.NatsURL, cfg.NatsCredsFile, credsFiles, rf.Conn.Connections,
		func(url, creds string) (*nats.Conn, error) {
			c, derr := natsutil.Connect(url, creds)
			if derr != nil {
				return nil, derr
			}
			return c.NatsConn(), nil
		},
	)
	if perr != nil {
		slog.Error("conn pool init", "error", perr)
		return 1
	}
	// C3: per-run UUIDv7. Stamped into every publish/request as
	// X-Loadgen-Run-ID so SUT-side traces/logs correlate. The actual
	// start_unix label on RunInfo is set AFTER readiness completes
	// (F9) so it agrees with the progress-reporter's elapsed/remaining
	// math; the gauge stays unset for the readiness window.
	runID := idgen.GenerateUUIDv7()
	slog.Info("run started",
		"run_id", runID, "preset", p.Name, "scenario", rf.Scenario,
		"rate", rf.Rate, "duration", rf.Duration.String())

	publisher := newNatsCorePublisher(pool, injectMode, js)
	publisher.runID = runID
	publisher.asyncJS = rf.JS.AsyncMaxPending > 0 // S5: enable async path when bound is set
	requester := &natsRequester{pool: pool, runID: runID}

	// Ramp + rate flags are validated above (before any NATS connect).

	// Phase 3 §3.3: readiness probe for read scenarios. Skipped for
	// messaging-pipeline (which doesn't request/reply to a service) and
	// when --skip-readiness is set.
	if !rf.Readiness.Skip && scenarioNeedsReadiness(rf.Scenario) && len(fixtures.Subscriptions) > 0 {
		probeSub := fixtures.Subscriptions[0]
		probe := buildReadinessProbe(rf.Scenario, &probeSub, cfg.SiteID, requester)
		probeCtx, probeCancel := context.WithTimeout(ctx, rf.Readiness.Timeout)
		if err := waitForReady(probeCtx, &readinessConfig{
			Probe: probe, MinBackoff: 200 * time.Millisecond, MaxBackoff: 2 * time.Second,
		}); err != nil {
			probeCancel()
			slog.Error("readiness probe failed", "scenario", rf.Scenario, "error", err)
			return 1
		}
		probeCancel()
		slog.Info("readiness probe succeeded", "scenario", rf.Scenario)
	}

	warmupDeadline := time.Now().Add(rf.Warmup)

	type runner interface {
		Run(ctx context.Context) error
	}
	var gen runner
	switch rf.Scenario {
	case "history-read":
		var msgIDs []string
		if rf.AutoWarmup.Enabled && needsAutoWarmup(rf.Scenario, &p) {
			slog.Info("auto-warmup phase starting",
				"rate", rf.AutoWarmup.Rate, "duration", rf.Warmup)
			// Bug 3: auto-warmup must always go through the frontdoor
			// even when --inject=canonical, otherwise PublishMsgAsync
			// targets a non-stream subject and the message-ID pool stays
			// empty.
			warmupPublisher := newWarmupPublisher(publisher)
			ids, werr := runAutoWarmup(ctx, &autoWarmupConfig{
				Preset: &p, Fixtures: fixtures, SiteID: cfg.SiteID,
				Rate:      rf.AutoWarmup.Rate,
				Publisher: warmupPublisher, Metrics: metrics, Collector: collector,
				Duration: rf.Warmup,
				Seed:     rf.Seed,
			})
			if werr != nil {
				slog.Warn("auto-warmup failed; proceeding with empty pool", "error", werr)
			} else {
				msgIDs = ids
				slog.Info("auto-warmup phase complete", "message_ids", len(msgIDs))
			}
			// Drop any unmatched correlation entries left over from the
			// warm-up phase so they don't inflate the read scenario's
			// final missing-reply / missing-broadcast count (B3).
			collector.PruneCorrelation()
			// Reset warmup deadline now that the warm-up phase has elapsed:
			// the read generator's measured window starts fresh.
			warmupDeadline = time.Now()
		}
		gen = NewHistoryReadGenerator(&HistoryReadConfig{
			Preset: &p, Fixtures: fixtures, SiteID: cfg.SiteID,
			Rate: rf.Rate, Requester: requester, Metrics: metrics,
			Collector:      collector,
			WarmupDeadline: warmupDeadline, MaxInFlight: cfg.MaxInFlight, Ramp: ramp,
			Timeout:    rf.RequestTimeout,
			MessageIDs: msgIDs,
		}, rf.Seed)
	case "search-read":
		gen = NewSearchReadGenerator(&SearchReadConfig{
			Preset: &p, Fixtures: fixtures, SiteID: cfg.SiteID,
			Rate: rf.Rate, Requester: requester, Metrics: metrics,
			Collector:      collector,
			WarmupDeadline: warmupDeadline, MaxInFlight: cfg.MaxInFlight, Ramp: ramp,
			Timeout: rf.RequestTimeout,
		}, rf.Seed)
	case "room-rpc":
		gen = NewRoomRPCGenerator(&RoomRPCConfig{
			Preset: &p, Fixtures: fixtures, SiteID: cfg.SiteID,
			Rate: rf.Rate, Requester: requester, Metrics: metrics,
			Collector:      collector,
			WarmupDeadline: warmupDeadline, MaxInFlight: cfg.MaxInFlight, Ramp: ramp,
			Timeout: rf.RequestTimeout,
		}, rf.Seed)
	default:
		gen = NewGenerator(&GeneratorConfig{
			Preset:         &p,
			Fixtures:       fixtures,
			SiteID:         cfg.SiteID,
			Rate:           rf.Rate,
			Inject:         injectMode,
			Publisher:      publisher,
			Metrics:        metrics,
			Collector:      collector,
			WarmupDeadline: warmupDeadline,
			MaxInFlight:    cfg.MaxInFlight,
			Ramp:           ramp,
			ConnIDFor: func(userID string) string {
				return strconv.Itoa(pool.IndexFor(userID))
			},
		}, rf.Seed)
	}

	// F9: capture runStart AFTER readiness completes so the RunInfo
	// gauge's start_unix label agrees with the progress reporter's
	// elapsed/remaining math.
	runStart := time.Now()
	metrics.RunInfo.WithLabelValues(
		runID, p.Name, rf.Scenario, strconv.FormatInt(runStart.Unix(), 10),
	).Set(1)

	runCtx, cancelRun := context.WithTimeout(ctx, rf.Duration)
	defer cancelRun()

	// F6: bridge runCtx → samplerCtx. samplerCtx is parented on the
	// SIGINT root (created earlier so samplers can run before the
	// generator); without this bridge, when runCtx cancels (timeout,
	// saturation abort, or liveness abort), samplers keep polling
	// JetStream — which blocks on dead NATS during a liveness-fail
	// drain. Bridge cancels samplers as soon as the run ends.
	go func() {
		<-runCtx.Done()
		cancelSamplers()
	}()

	// Phase 3 §3.2: live progress reporter. Off when interval <= 0.
	var progressWG sync.WaitGroup
	if rf.Progress.Interval > 0 {
		progressTicker := time.NewTicker(rf.Progress.Interval)
		defer progressTicker.Stop()
		progressWG.Add(1)
		go func() {
			defer progressWG.Done()
			runProgress(runCtx, &progressConfig{
				Metrics: metrics, Preset: p.Name,
				Logger:      slog.Default(),
				Ticks:       progressTicker.C,
				Window:      latencyWindow,
				WindowOver:  rf.Abort.P99Sustain,
				RunStart:    runStart,
				RunDuration: rf.Duration,
				TargetRate:  rf.Rate,
			})
		}()
	}

	// Phase 3 §3.5: saturation auto-detect.
	var abortTripped atomic.Bool
	var abortReason atomic.Value // string
	var abortWG sync.WaitGroup
	if rf.Abort.P99Ms > 0 || rf.Abort.ErrorPct > 0 {
		abortWG.Add(1)
		go func() {
			defer abortWG.Done()
			abortCfg := &abortConfig{
				Window:       latencyWindow,
				P99Limit:     time.Duration(rf.Abort.P99Ms) * time.Millisecond,
				P99Sustain:   rf.Abort.P99Sustain,
				ErrorPct:     rf.Abort.ErrorPct,
				ErrorSustain: rf.Abort.ErrorSustain,
			}
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case t := <-ticker.C:
					if tripped, reason := abortShouldFire(abortCfg, t); tripped {
						slog.Warn("abort fired", "reason", reason)
						abortTripped.Store(true)
						abortReason.Store(reason)
						cancelRun()
						return
					}
				}
			}
		}()
	}

	// C5 + F1: mid-run liveness watcher. Probes the SUT periodically.
	// For messaging-pipeline (no RPC target), falls back to a NATS RTT
	// check on the observer conn so the same "SUT is gone" detector
	// works for every scenario. Probes use the OBSERVER connection
	// regardless of --connections / --nats-creds-dir so per-user creds
	// rotation can't cause permission-denied false-positives (F3 fix).
	var livenessFailed atomic.Bool
	var livenessReason atomic.Value
	var livenessWG sync.WaitGroup
	if rf.Liveness.Interval > 0 && len(fixtures.Subscriptions) > 0 {
		probeSub := fixtures.Subscriptions[0]
		// Observer-routed requester so the probe uses the global creds
		// (cfg.NatsCredsFile) regardless of pool fan-out and rotation.
		observerPool := &ConnPool{observer: pool.Observer()}
		observerReq := &natsRequester{pool: observerPool, runID: runID}
		liveProbe := buildLivenessProbe(rf.Scenario, &probeSub, cfg.SiteID, observerReq, pool.Observer())
		livenessWG.Add(1)
		go func() {
			defer livenessWG.Done()
			runLiveness(runCtx, &livenessConfig{
				Probe:            liveProbe,
				Interval:         rf.Liveness.Interval,
				ConsecutiveFails: rf.Liveness.Failures,
				Timeout:          rf.Liveness.Timeout,
				Counter: func(result string) {
					metrics.LivenessProbes.WithLabelValues(p.Name, result).Inc()
				},
			}, &livenessFailed, func(reason string) {
				slog.Warn("liveness watcher fired", "reason", reason)
				livenessReason.Store(reason)
				cancelRun()
			})
		}()
	}

	genErr := gen.Run(runCtx)
	progressWG.Wait()
	abortWG.Wait()
	livenessWG.Wait()

	// S5: when async JS publishes are enabled, the generator's Run-return
	// only guarantees publishes are queued; their PubAcks may still be
	// in flight. Wait for the in-flight window to drain before exiting so
	// errors land in loadgen_publish_errors_total{reason="async_ack"}
	// (and orphan messageIDs are evicted from the broadcast collector)
	// before the report is emitted. Bounded by asyncDrainTimeout so a
	// wedged stream doesn't hang the run forever.
	//
	// ORDERING INVARIANT (R2 WARN #1): no js.PublishMsgAsync may be
	// invoked after this point — the gen.Run goroutines have all returned
	// (drainGracePeriod inside Run) and no other code path here calls
	// PublishMsgAsync. If a future change adds a late publisher (a new
	// sampler, liveness probe, etc.), it MUST run before this drain or
	// late acks will race with report emission.
	if publisher.asyncJS {
		select {
		case <-js.PublishAsyncComplete():
		case <-time.After(asyncDrainTimeout):
			slog.Warn("jetstream async publish drain timed out",
				"pending", js.PublishAsyncPending(),
				"max_pending", rf.JS.AsyncMaxPending,
				"drain_timeout", asyncDrainTimeout.String(),
			)
		}
	}

	// Trailing replies and broadcasts may still be in flight after gen.Run
	// returns (the SUT's reply latency is independent of the generator's
	// stop). Replace the previous time.Sleep(2s) — which was racy and
	// CLAUDE.md-prohibited — with a deterministic quiescence detector:
	// poll the Collector's outstanding correlation count every 50ms,
	// declare drained when it stops decreasing for 500ms, cap at 5s.
	drainTrailingReplies(collector, 5*time.Second, 50*time.Millisecond, 10)
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
	// Bug 2: per-scenario measured-window math.
	//   - messaging-pipeline: warmup is carved out of the run window, so
	//     measured = duration - warmup.
	//   - read scenarios with auto-warmup: warmup ran BEFORE runCtx, then
	//     warmupDeadline was reset (line ~496) and the read scenario used
	//     the full rf.Duration. Use time.Since(warmupDeadline) clamped to
	//     [0, rf.Duration] so an early-abort run still reports the fraction
	//     of the window that actually elapsed.
	//   - read scenarios without auto-warmup: warmupDeadline is still in
	//     the future relative to runStart, so the same Since(warmupDeadline)
	//     formula collapses to the post-warmup measured window.
	measured := time.Since(warmupDeadline)
	if measured > rf.Duration {
		measured = rf.Duration
	}
	requestStats := collector.RequestStats()
	actualRate := computeActualRate(rf.Scenario, injectMode, measured,
		collector.E1Count(), missingReplies, sentMeasured, requestStats)

	summary := Summary{
		RunID:             runID,
		Preset:            p.Name,
		Seed:              rf.Seed,
		Site:              cfg.SiteID,
		TargetRate:        rf.Rate,
		ActualRate:        actualRate,
		Duration:          rf.Duration,
		Warmup:            rf.Warmup,
		Inject:            rf.Inject,
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
		Consumers:         consumerSnapshots(samplers),
		Requests:          requestStats,
	}
	if err := PrintSummary(os.Stdout, &summary); err != nil {
		slog.Warn("print summary", "error", err)
	}

	if rf.CSV != "" {
		if err := writeCSVFile(rf.CSV, runID, collector); err != nil {
			slog.Error("csv export", "error", err)
		}
	}

	totalErrs := summary.PublishErrors + summary.GatekeeperErrors + summary.MissingReplies + summary.MissingBroadcasts
	// F7: log emission follows the exit-code precedence. When both
	// abort + liveness trip near-simultaneously the user gets the
	// higher-priority message only, matching the exit code.
	switch {
	case livenessFailed.Load():
		reason, _ := livenessReason.Load().(string)
		slog.Error("run aborted by liveness watcher (SUT unreachable)", "reason", reason)
	case abortTripped.Load():
		reason, _ := abortReason.Load().(string)
		slog.Warn("run aborted by saturation watcher", "reason", reason)
	}
	return exitCodeForFull(abortTripped.Load(), livenessFailed.Load(), summary.SentMeasured, totalErrs)
}

// exitCodeFor maps the run's terminal state to the process exit code.
// Phase 3 §3.5: abort wins over clean-fail wins over clean-pass.
//
//	0 = clean pass (no errors above tolerance, no abort)
//	1 = clean fail (errors above tolerance)
//	2 = aborted by the saturation watcher
//
// Extracted out of runRun so the policy is unit-testable in isolation
// (see TestRunRun_ExitCodeOnAbort).
func exitCodeFor(aborted bool, sent, totalErrs int) int {
	return exitCodeForFull(aborted, false, sent, totalErrs)
}

// exitCodeForFull is the policy with the C5 liveness path added.
// Precedence: liveness > saturation > clean-fail > clean-pass.
//
//	0 = clean pass (no errors above tolerance, no abort, SUT healthy)
//	1 = clean fail (errors above tolerance, no abort)
//	2 = aborted by the saturation watcher (SUT got slow)
//	3 = aborted by the liveness watcher (SUT became unreachable)
func exitCodeForFull(saturationAborted, livenessFailed bool, sent, totalErrs int) int {
	if livenessFailed {
		return 3
	}
	if saturationAborted {
		return 2
	}
	return DetermineExitCode(sent, totalErrs)
}

// resolveAbortPeakRPS returns the peak rate the abort watcher must
// retain samples for. When a ramp is configured (--ramp-to > 0), use
// max(--rate, --ramp-to) so an ascending ramp doesn't deafen the watcher
// mid-run. Bug 6.
func resolveAbortPeakRPS(rate, rampTo int) int {
	if rampTo > rate {
		return rampTo
	}
	return rate
}

// resolveAbortWindowMaxSamples returns the effective ring-buffer cap.
//
// Bug 6: the registered default of 10_000 deafens the abort watcher
// at the default --rate=500 × --abort-p99-sustain=30s = 15_000. When
// the user did NOT pass --abort-window-max-samples, auto-size to
// 2 × peak_rps × max_sustain (the README's rule of thumb). Otherwise
// honor the user's value verbatim — including 0 (cap disabled).
//
// When peak_rps or maxSustain is zero, fall back to the registered
// default (no traffic / no sustain ⇒ no need to inflate).
func resolveAbortWindowMaxSamples(userSet bool, userVal, peakRPS int, maxSustain time.Duration) int {
	if userSet {
		return userVal
	}
	if peakRPS <= 0 || maxSustain <= 0 {
		return userVal
	}
	return 2 * peakRPS * int(maxSustain.Seconds())
}

// computeActualRate returns the achieved request rate (req/sec) over the
// measured window. Bug 2: pre-fix, the formula was hard-wired to the
// messaging-pipeline correlation counts (E1+missing or sentMeasured for
// canonical), which read 0 for read scenarios because no E1/JS-publish
// traffic flows there. Read scenarios now sum per-(scenario, kind)
// post-warmup counts from the Collector instead.
//
// Returns 0 when measured <= 0 to avoid divide-by-zero in early-abort
// or warmup-only runs.
func computeActualRate(scenario string, inject InjectMode, measured time.Duration,
	e1Count, missingReplies, sentMeasured int, requestStats []RequestStat,
) float64 {
	if measured <= 0 {
		return 0
	}
	secs := measured.Seconds()
	if scenarioNeedsReadiness(scenario) {
		// Read scenarios: count post-warmup requests across all kinds for
		// this scenario. RequestStats are already DiscardBefore-trimmed.
		total := 0
		for _, rs := range requestStats {
			if rs.Scenario == scenario {
				total += rs.Count
			}
		}
		return float64(total) / secs
	}
	// Messaging-pipeline path.
	switch inject {
	case InjectCanonical:
		// In canonical mode, byReqID is never populated, so E1Count and
		// missingReplies are both 0; sentMeasured is the only ground truth.
		return float64(sentMeasured) / secs
	default:
		return float64(e1Count+missingReplies) / secs
	}
}

// asyncDrainTimeout caps the wait for in-flight async JetStream
// publishes to ack at shutdown. Same magnitude as drainTrailingReplies
// so the two shutdown phases are symmetric; both bounded so a wedged
// stream doesn't hang the run forever.
const asyncDrainTimeout = 5 * time.Second

func lastToken(subj string) string {
	i := strings.LastIndex(subj, ".")
	if i < 0 {
		return subj
	}
	return subj[i+1:]
}

// drainTrailingReplies waits for trailing replies/broadcasts to land
// after gen.Run returns. Polls the Collector's outstanding correlation
// count every `interval`; when it stops decreasing for `stableTicks`
// consecutive samples, declares drained. Caps total wait at maxWait.
//
// Replaces the previous time.Sleep(2s), which was racy (workers'
// drainGracePeriod is 5s, so up to 3s of late samples were lost) and
// violated CLAUDE.md §3 ("Never use time.Sleep for goroutine sync").
func drainTrailingReplies(c *Collector, maxWait, interval time.Duration, stableTicks int) {
	deadlineCh := time.After(maxWait)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	prev := c.outstandingCorrelations()
	stable := 0
	for {
		select {
		case <-deadlineCh:
			return
		case <-ticker.C:
			cur := c.outstandingCorrelations()
			if cur == 0 {
				return
			}
			if cur == prev {
				stable++
				if stable >= stableTicks {
					return
				}
			} else {
				stable = 0
			}
			prev = cur
		}
	}
}

func writeCSVFile(path, runID string, c *Collector) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create csv: %w", err)
	}
	defer func() { _ = f.Close() }()
	var rows []CSVSample
	for i, d := range c.E1Samples() {
		rows = append(rows, CSVSample{RunID: runID, RowIndex: int64(i), Metric: "E1", LatencyNs: d.Nanoseconds()})
	}
	for i, d := range c.E2Samples() {
		rows = append(rows, CSVSample{RunID: runID, RowIndex: int64(i), Metric: "E2", LatencyNs: d.Nanoseconds()})
	}
	for i, r := range c.RequestSampleRows() {
		rows = append(rows, CSVSample{
			RunID:     runID,
			RowIndex:  int64(i),
			Metric:    r.Scenario + "." + r.Kind,
			LatencyNs: r.Latency.Nanoseconds(),
		})
	}
	return WriteCSV(f, rows)
}

func consumerSnapshots(samplers []*ConsumerSampler) []ConsumerStat {
	out := make([]ConsumerStat, 0, len(samplers))
	for _, s := range samplers {
		out = append(out, s.Snapshot())
	}
	return out
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
