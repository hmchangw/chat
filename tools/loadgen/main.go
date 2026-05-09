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
	scenario := fs.String("scenario", "messaging-pipeline", "scenario: messaging-pipeline|history-read|search-read|room-rpc")
	requestTimeout := fs.Duration("request-timeout", 5*time.Second, "per-request timeout for read scenarios")
	autoWarmup := fs.Bool("auto-warmup", true, "run a brief messaging-pipeline phase to populate message IDs before read scenarios that need them")
	autoWarmupRate := fs.Int("auto-warmup-rate", 200, "publish rate (rps) during the auto-warmup phase")
	progressInterval := fs.Duration("progress-interval", 10*time.Second, "live progress log interval; 0 disables")
	skipReadiness := fs.Bool("skip-readiness", false, "skip the pre-run readiness probe for read scenarios")
	readinessTimeout := fs.Duration("readiness-timeout", 30*time.Second, "deadline for the readiness probe to succeed")
	rampFrom := fs.Int("ramp-from", 0, "starting rate (rps) for a ramped run; 0 disables ramping")
	rampTo := fs.Int("ramp-to", 0, "ending rate (rps) for a ramped run; 0 disables ramping")
	rampDuration := fs.Duration("ramp-duration", 0, "time to climb from --ramp-from to --ramp-to")
	rampShape := fs.String("ramp-shape", "linear", "ramp curve: linear|exponential")
	connections := fs.Int("connections", 1, "number of NATS data connections (per-user fan-out); 1 reuses the observer connection")
	abortP99Ms := fs.Int("abort-on-p99-ms", 0, "abort the run if the p99 of the abort window's latency stays over this for --abort-p99-sustain; 0 disables")
	abortP99Sustain := fs.Duration("abort-p99-sustain", 30*time.Second, "sustain window for the p99 abort threshold")
	abortErrorPct := fs.Float64("abort-on-error-pct", 0, "abort the run if error rate stays over this fraction (0..1) for --abort-error-sustain; 0 disables")
	abortErrorSustain := fs.Duration("abort-error-sustain", 10*time.Second, "sustain window for the error-rate abort threshold")
	csvPath := fs.String("csv", "", "optional csv output path")
	_ = fs.Parse(args)
	switch *scenario {
	case "messaging-pipeline", "history-read", "search-read", "room-rpc":
	default:
		fmt.Fprintf(os.Stderr, "unknown scenario: %s\n", *scenario)
		return 2
	}
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
	// Phase 3 §3.5: in-process latency ring buffer for the abort watcher.
	// Sized to retain max(--abort-p99-sustain, --abort-error-sustain, 60s).
	windowRetain := 60 * time.Second
	if *abortP99Sustain > windowRetain {
		windowRetain = *abortP99Sustain
	}
	if *abortErrorSustain > windowRetain {
		windowRetain = *abortErrorSustain
	}
	latencyWindow := NewLatencyWindow(windowRetain)
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
	pool, perr := NewConnPoolWithObserver(
		nc.NatsConn(), cfg.NatsURL, cfg.NatsCredsFile, *connections,
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
	publisher := newNatsCorePublisher(pool, injectMode, js)
	requester := &natsRequester{pool: pool}

	// Phase 3 §3.4: optional rate ramp.
	var ramp *Ramp
	if *rampFrom > 0 || *rampTo > 0 || *rampDuration > 0 {
		var shape RampShape
		switch *rampShape {
		case "linear":
			shape = RampLinear
		case "exponential":
			shape = RampExponential
		default:
			fmt.Fprintf(os.Stderr, "unknown ramp shape: %s (want linear|exponential)\n", *rampShape)
			return 2
		}
		if *rampFrom <= 0 || *rampTo <= 0 || *rampDuration <= 0 {
			fmt.Fprintln(os.Stderr, "--ramp-from, --ramp-to, --ramp-duration must all be > 0 when ramping")
			return 2
		}
		ramp = &Ramp{From: *rampFrom, To: *rampTo, Duration: *rampDuration, Shape: shape}
	}
	if err := validateRampVsRate(*rate, ramp); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}

	// Phase 3 §3.3: readiness probe for read scenarios. Skipped for
	// messaging-pipeline (which doesn't request/reply to a service) and
	// when --skip-readiness is set.
	if !*skipReadiness && scenarioNeedsReadiness(*scenario) && len(fixtures.Subscriptions) > 0 {
		probeSub := fixtures.Subscriptions[0]
		probe := buildReadinessProbe(*scenario, &probeSub, cfg.SiteID, requester)
		probeCtx, probeCancel := context.WithTimeout(ctx, *readinessTimeout)
		if err := waitForReady(probeCtx, &readinessConfig{
			Probe: probe, MinBackoff: 200 * time.Millisecond, MaxBackoff: 2 * time.Second,
		}); err != nil {
			probeCancel()
			slog.Error("readiness probe failed", "scenario", *scenario, "error", err)
			return 1
		}
		probeCancel()
		slog.Info("readiness probe succeeded", "scenario", *scenario)
	}

	warmupDeadline := time.Now().Add(*warmup)

	type runner interface {
		Run(ctx context.Context) error
	}
	var gen runner
	switch *scenario {
	case "history-read":
		var msgIDs []string
		if *autoWarmup && needsAutoWarmup(*scenario, &p) {
			slog.Info("auto-warmup phase starting",
				"rate", *autoWarmupRate, "duration", *warmup)
			ids, werr := runAutoWarmup(ctx, &autoWarmupConfig{
				Preset: &p, Fixtures: fixtures, SiteID: cfg.SiteID,
				Rate:      *autoWarmupRate,
				Publisher: publisher, Metrics: metrics, Collector: collector,
				Duration: *warmup,
				Seed:     *seed,
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
			Rate: *rate, Requester: requester, Metrics: metrics,
			Collector:      collector,
			WarmupDeadline: warmupDeadline, MaxInFlight: cfg.MaxInFlight, Ramp: ramp,
			Timeout:    *requestTimeout,
			MessageIDs: msgIDs,
		}, *seed)
	case "search-read":
		gen = NewSearchReadGenerator(&SearchReadConfig{
			Preset: &p, Fixtures: fixtures, SiteID: cfg.SiteID,
			Rate: *rate, Requester: requester, Metrics: metrics,
			Collector:      collector,
			WarmupDeadline: warmupDeadline, MaxInFlight: cfg.MaxInFlight, Ramp: ramp,
			Timeout: *requestTimeout,
		}, *seed)
	case "room-rpc":
		gen = NewRoomRPCGenerator(&RoomRPCConfig{
			Preset: &p, Fixtures: fixtures, SiteID: cfg.SiteID,
			Rate: *rate, Requester: requester, Metrics: metrics,
			Collector:      collector,
			WarmupDeadline: warmupDeadline, MaxInFlight: cfg.MaxInFlight, Ramp: ramp,
			Timeout: *requestTimeout,
		}, *seed)
	default:
		gen = NewGenerator(&GeneratorConfig{
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
			ConnIDFor: func(userID string) string {
				return strconv.Itoa(pool.IndexFor(userID))
			},
		}, *seed)
	}

	runCtx, cancelRun := context.WithTimeout(ctx, *duration)
	defer cancelRun()

	// Phase 3 §3.2: live progress reporter. Off when interval <= 0.
	var progressWG sync.WaitGroup
	if *progressInterval > 0 {
		progressTicker := time.NewTicker(*progressInterval)
		defer progressTicker.Stop()
		progressWG.Add(1)
		go func() {
			defer progressWG.Done()
			runProgress(runCtx, &progressConfig{
				Metrics: metrics, Preset: p.Name,
				Logger: slog.Default(), Ticks: progressTicker.C,
			})
		}()
	}

	// Phase 3 §3.5: saturation auto-detect.
	var abortTripped atomic.Bool
	var abortReason atomic.Value // string
	var abortWG sync.WaitGroup
	if *abortP99Ms > 0 || *abortErrorPct > 0 {
		abortWG.Add(1)
		go func() {
			defer abortWG.Done()
			abortCfg := &abortConfig{
				Window:       latencyWindow,
				P99Limit:     time.Duration(*abortP99Ms) * time.Millisecond,
				P99Sustain:   *abortP99Sustain,
				ErrorPct:     *abortErrorPct,
				ErrorSustain: *abortErrorSustain,
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

	genErr := gen.Run(runCtx)
	progressWG.Wait()
	abortWG.Wait()

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
		Consumers:         consumerSnapshots(samplers),
		Requests:          collector.RequestStats(),
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
	if abortTripped.Load() {
		// Exit code 2 distinguishes "saturated" from clean-pass (0) and
		// clean-fail (1). Phase 3 §3.5.
		reason, _ := abortReason.Load().(string)
		slog.Warn("run aborted by saturation watcher", "reason", reason)
		return 2
	}
	return DetermineExitCode(summary.SentMeasured, totalErrs)
}

type natsCorePublisher struct {
	pool         *ConnPool // Phase 3 §3.6 — picks the data conn per subject's userID
	useJetStream bool
	js           jetstream.JetStream
}

// natsRequester adapts nc.RequestWithContext to the Requester interface
// used by the read-only scenarios. The timeout is enforced via a per-call
// derived context so callers don't need to thread one in themselves.
// When the pool's Size > 1, the request is routed to the data connection
// hashed from the subject's user-account segment.
type natsRequester struct {
	pool *ConnPool
}

func (r *natsRequester) Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	conn := r.pool.For(UserFromSubject(subject))
	msg, err := conn.RequestWithContext(ctx, subject, data)
	if err != nil {
		return nil, fmt.Errorf("nats request: %w", err)
	}
	return msg.Data, nil
}

func newNatsCorePublisher(pool *ConnPool, inject InjectMode, js jetstream.JetStream) *natsCorePublisher {
	return &natsCorePublisher{pool: pool, useJetStream: inject == InjectCanonical, js: js}
}

func (p *natsCorePublisher) Publish(ctx context.Context, subject string, data []byte) error {
	if p.useJetStream {
		// JetStream publishes go through one writer; canonical-injection
		// is a single-stream concern, not a per-user concern.
		if _, err := p.js.Publish(ctx, subject, data); err != nil {
			return fmt.Errorf("jetstream publish: %w", err)
		}
		return nil
	}
	conn := p.pool.For(UserFromSubject(subject))
	if err := conn.Publish(subject, data); err != nil {
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
	for i, r := range c.RequestSampleRows() {
		rows = append(rows, CSVSample{
			TimestampNs: int64(i),
			Metric:      r.Scenario + "." + r.Kind,
			LatencyNs:   r.Latency.Nanoseconds(),
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
