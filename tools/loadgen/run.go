package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// executeRun is the heart of the run subcommand. It receives an already-
// constructed Runtime (NATS conn, JetStream client, Prometheus metrics, HTTP
// servers) plus the validated run flags and preset, and performs:
//
//   - E1/E2 subscription setup (gatekeeper replies, broadcast events)
//   - consumer-lag sampler goroutines
//   - full ConnPool construction (with optional per-user creds rotation)
//   - publisher and requester construction
//   - readiness probe (if required by the scenario)
//   - generator construction and run loop (gen.Run)
//   - saturation-abort watcher and liveness watcher goroutines
//   - post-run drain (async JS acks, trailing replies/broadcasts)
//   - summary print and optional CSV export
//   - exit code determination
//
// It returns the process exit code (0=pass, 1=fail, 2=saturated, 3=dead SUT,
// 4=UNTRUSTED verdict on otherwise-clean run).
func executeRun(ctx context.Context, rt *Runtime, rf *runFlags, p *Preset, injectMode InjectMode) (int, Summary) {
	cfg := rt.cfg
	runID := rt.RunID()
	metrics := rt.Metrics()

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
	js, err := jetstream.New(rt.NC().NatsConn(), jsOpts...)
	if err != nil {
		slog.Error("jetstream init", "error", err)
		return 1, Summary{}
	}

	fixtures := BuildFixtures(p, rf.Seed, cfg.SiteID)

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
	rampTo := 0
	if rf.BuiltRamp != nil {
		rampTo = rf.BuiltRamp.To
	}
	resolvedAbortCap := resolveAbortWindowMaxSamples(
		rf.Abort.WindowMaxSamplesSet, rf.Abort.WindowMaxSamples,
		resolveAbortPeakRPS(rf.Rate, rampTo), abortMaxSustain,
	)
	if resolvedAbortCap != rf.Abort.WindowMaxSamples {
		slog.Info("abort-window-max-samples auto-sized",
			"original", rf.Abort.WindowMaxSamples, "resolved", resolvedAbortCap,
			"peak_rps", resolveAbortPeakRPS(rf.Rate, rampTo),
			"max_sustain", abortMaxSustain.String())
	}
	latencyWindow := NewLatencyWindow(windowRetain).WithMaxSamples(resolvedAbortCap)
	collector.AttachWindow(latencyWindow)

	// warmupDeadlineNs stores the warmup deadline as Unix nanoseconds using
	// atomic.Int64 so the E1 NATS-dispatch goroutine can read it safely while
	// executeRun writes it (Fix 4: was a plain time.Time causing a data race).
	// Zero value is safe: time.Now().UnixNano() > 0, so Before(zero) is always
	// false and every reply is labelled "measured" until the deadline is set.
	var warmupDeadlineNs atomic.Int64

	// E1 subscription: gatekeeper replies.
	e1Sub, err := rt.NC().NatsConn().Subscribe(subject.UserResponseWildcard(), func(msg *nats.Msg) {
		// Filter: when the reply carries a run-ID header from a different run
		// (concurrent loadgen on the same machine), silently ignore it so we
		// don't accidentally credit another run's replies to ours.
		if hdr := msg.Header.Get(HeaderRunID); hdr != "" && hdr != runID {
			return
		}
		reqID := lastToken(msg.Subject)
		var payload struct {
			Error string `json:"error"`
		}
		e1Phase := "measured"
		if dl := warmupDeadlineNs.Load(); dl > 0 && time.Now().UnixNano() < dl {
			e1Phase = "warmup"
		}
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			// Malformed reply; count and drop per spec.
			metrics.PublishErrors.WithLabelValues(p.Name, e1Phase, "bad_reply").Inc()
			return
		}
		if payload.Error != "" {
			metrics.PublishErrors.WithLabelValues(p.Name, e1Phase, "gatekeeper").Inc()
		}
		collector.RecordReply(reqID, time.Now())
	})
	if err != nil {
		slog.Error("subscribe e1", "error", err)
		return 1, Summary{}
	}
	defer func() { _ = e1Sub.Unsubscribe() }()

	// E2 subscription: broadcast events.
	e2Handler := func(msg *nats.Msg) {
		// Filter: ignore broadcasts that originated from a different loadgen
		// run so concurrent runs on the same machine don't credit each
		// other's events.
		if hdr := msg.Header.Get(HeaderRunID); hdr != "" && hdr != runID {
			return
		}
		var evt model.RoomEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		if evt.Message == nil || evt.Message.ID == "" {
			return
		}
		collector.RecordBroadcast(evt.Message.ID, time.Now())
	}

	e2Sub, err := rt.NC().NatsConn().Subscribe(subject.RoomEventWildcard(), e2Handler)
	if err != nil {
		slog.Error("subscribe e2", "error", err)
		return 1, Summary{}
	}
	defer func() { _ = e2Sub.Unsubscribe() }()

	// Broadcast-worker emits DM broadcasts on chat.user.{account}.event.room
	// (see pkg/subject.UserRoomEvent). Subscribe to both so E2 correlation
	// covers both group and DM rooms.
	e2DMSub, err := rt.NC().NatsConn().Subscribe(subject.UserRoomEventWildcard(), e2Handler)
	if err != nil {
		slog.Error("subscribe e2 dm", "error", err)
		return 1, Summary{}
	}
	defer func() { _ = e2DMSub.Unsubscribe() }()

	canonical := stream.MessagesCanonical(cfg.SiteID)
	rooms := stream.Rooms(cfg.SiteID)
	inbox := stream.Inbox(cfg.SiteID)
	samplerCtx, cancelSamplers := context.WithCancel(ctx)
	defer cancelSamplers()
	samplers := []*ConsumerSampler{
		// MESSAGES_CANONICAL consumers (driven by messaging-pipeline).
		// Names are prefixed with the run ID so concurrent runs on the same
		// machine each observe their own durable consumer lag without
		// interfering with each other or with the production consumers.
		NewConsumerSampler(js, canonical.Name, consumerName(runID, "message-worker"), metrics, 1*time.Second),
		NewConsumerSampler(js, canonical.Name, consumerName(runID, "broadcast-worker"), metrics, 1*time.Second),
		NewConsumerSampler(js, canonical.Name, consumerName(runID, "notification-worker"), metrics, 1*time.Second),
		NewConsumerSampler(js, canonical.Name, consumerName(runID, "search-sync-worker-messages"), metrics, 1*time.Second),
		// ROOMS consumer (driven by room-rpc).
		NewConsumerSampler(js, rooms.Name, consumerName(runID, "room-worker"), metrics, 1*time.Second),
		// INBOX consumers — populated either by federation or by local-publish
		// from message-worker / room-worker. Empty gauges read 0 cleanly when
		// the relevant scenario isn't running.
		NewConsumerSampler(js, inbox.Name, consumerName(runID, "search-sync-worker-spotlight"), metrics, 1*time.Second),
		NewConsumerSampler(js, inbox.Name, consumerName(runID, "search-sync-worker-user-room"), metrics, 1*time.Second),
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
		return 1, Summary{}
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
			return 2, Summary{}
		}
		slog.Warn("--nats-creds-dir ignored: --connections=1 routes all traffic through the observer (cfg.NatsCredsFile)",
			"creds_count", len(credsFiles))
	}
	pool, perr := NewConnPoolWithCreds(
		rt.NC().NatsConn(), cfg.NatsURL, cfg.NatsCredsFile, credsFiles, rf.Conn.Connections,
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
		return 1, Summary{}
	}
	// C3: per-run UUIDv7. Stamped into every publish/request as
	// X-Loadgen-Run-ID so SUT-side traces/logs correlate. The actual
	// start_unix label on RunInfo is set AFTER readiness completes
	// (F9) so it agrees with the progress-reporter's elapsed/remaining
	// math; the gauge stays unset for the readiness window.
	slog.Info("run started",
		"run_id", runID, "preset", p.Name, "scenario", rf.Scenario,
		"rate", rf.Rate, "duration", rf.Duration.String())

	publisher := newNatsCorePublisher(pool, injectMode, js)
	publisher.runID = runID
	publisher.asyncJS = rf.JS.AsyncMaxPending > 0 // S5: enable async path when bound is set
	requester := &natsRequester{pool: pool, runID: runID}

	// Ramp + rate flags are validated in runRun (before any NATS connect).

	// Build the ScenarioDeps adapter that exposes run-local state to the
	// scenario registry. Constructed here so it can be passed to readiness
	// and liveness probes, which are invoked before the generator is built.
	// warmupDeadline and msgIDs are set below (after readiness and auto-warmup).
	deps := &runDeps{
		publisher:   publisher,
		requester:   requester,
		collector:   collector,
		metrics:     metrics,
		fixtures:    &fixtures,
		preset:      p,
		siteID:      cfg.SiteID,
		maxInFlight: cfg.MaxInFlight,
		omission:    rt.Omission(),
		injectMode:  injectMode,
		connIDFor:   func(userID string) string { return strconv.Itoa(pool.IndexFor(userID)) },
		warmupPubl:  newWarmupPublisher(publisher),
		rt:          rt,
	}

	// Dispatch to the registered scenario. All four built-in scenarios were
	// registered via their init() functions in scenario_*.go files.
	sc, ok := LookupScenario(rf.Scenario)
	if !ok {
		slog.Error("unknown scenario", "scenario", rf.Scenario, "available", scenarioNames())
		return 2, Summary{}
	}
	factory, ok := sc.(GeneratorFactory)
	if !ok {
		slog.Error("scenario has no generator factory", "scenario", rf.Scenario)
		return 2, Summary{}
	}

	// Phase 3 §3.3: readiness probe for read scenarios. Skipped for
	// messaging-pipeline (which doesn't implement ReadinessProber) and
	// when --skip-readiness is set.
	if !rf.Readiness.Skip && len(fixtures.Subscriptions) > 0 {
		if pr, hasprobe := sc.(ReadinessProber); hasprobe {
			probe := pr.BuildReadinessProbe(deps)
			probeCtx, probeCancel := context.WithTimeout(ctx, rf.Readiness.Timeout)
			if err := waitForReady(probeCtx, &readinessConfig{
				Probe: probe, MinBackoff: 200 * time.Millisecond, MaxBackoff: 2 * time.Second,
			}); err != nil {
				probeCancel()
				slog.Error("readiness probe failed", "scenario", rf.Scenario, "error", err)
				return 1, Summary{}
			}
			probeCancel()
			slog.Info("readiness probe succeeded", "scenario", rf.Scenario)
		}
	}

	warmupDeadline := time.Now().Add(rf.Warmup)
	warmupDeadlineNs.Store(warmupDeadline.UnixNano())

	// Auto-warmup: let the scenario declare whether it needs a brief
	// messaging-pipeline phase to populate the message-ID pool.
	var msgIDs []string
	if rf.AutoWarmup.Enabled {
		if aw, ok := sc.(AutoWarmer); ok && aw.NeedsAutoWarmup(p) {
			slog.Info("auto-warmup phase starting",
				"rate", rf.AutoWarmup.Rate, "duration", rf.Warmup)
			// Bug 3: auto-warmup must always go through the frontdoor
			// even when --inject=canonical, otherwise PublishMsgAsync
			// targets a non-stream subject and the message-ID pool stays
			// empty.
			ids, werr := runAutoWarmup(ctx, &autoWarmupConfig{
				Preset: p, Fixtures: fixtures, SiteID: cfg.SiteID,
				Rate:      rf.AutoWarmup.Rate,
				Publisher: deps.WarmupPublisher(), Metrics: metrics, Collector: collector,
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
			warmupDeadlineNs.Store(warmupDeadline.UnixNano())
		}
	}
	// Pass harvested message IDs and the (possibly-reset) warmupDeadline to
	// deps so the generator factories (NewGenerator) can consume them.
	deps.msgIDs = msgIDs
	deps.warmupDeadline = warmupDeadline

	gen, err := factory.NewGenerator(deps, rf)
	if err != nil {
		slog.Error("build generator", "error", err.Error())
		return 1, Summary{}
	}

	// Settle phase: runs AFTER warmup, BEFORE the measured window starts.
	// Probes N recent message IDs from the auto-warmup pool to ensure they
	// are visible in the downstream system (read-after-write fence).
	// When rf.Settle.Probes == 0 or no IDs are available, Settle returns
	// immediately with AllSucceeded == true (no-op).
	//
	// Phase 1a.6: uses an always-OK stub probe so the infrastructure is
	// in place; the real LoadHistory probe is wired in Phase 3.1 (RAW).
	// The settle outcome is fed into RunQualityInputs.SettleOutcome so a
	// failed settle downgrades the verdict to UNTRUSTED.
	recentIDs := collector.MessageIDs()
	if len(recentIDs) > rf.Settle.Probes && rf.Settle.Probes > 0 {
		recentIDs = recentIDs[len(recentIDs)-rf.Settle.Probes:]
	}
	// TODO Phase 3.1: replace this stub with a real LoadHistory probe so
	// settle actually verifies message visibility in Cassandra.
	alwaysOKProbe := func(_ context.Context, _ string) error { return nil }
	settleCtx, settleCancel := context.WithTimeout(ctx, rf.Settle.Timeout)
	settleOutcome, settleErr := rt.Settle(settleCtx, rf.Settle, recentIDs, alwaysOKProbe)
	settleCancel()
	// Store settle outcome on Runtime so Finalize can include it in the bundle.
	rt.SetLastSettle(settleOutcome)
	if settleErr != nil {
		slog.Warn("settle phase failed", "error", settleErr,
			"succeeded", settleOutcome.Succeeded, "total", len(settleOutcome.Probes))
	} else if rf.Settle.Probes > 0 && len(recentIDs) > 0 {
		slog.Info("settle phase complete", "probes", settleOutcome.Succeeded)
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
	startAbortWatcher(runCtx, rf, latencyWindow, cancelRun, &abortTripped, &abortReason, &abortWG)

	// C5 + F1: mid-run liveness watcher. Probes the SUT periodically.
	// For messaging-pipeline (no RPC target), falls back to a NATS RTT
	// check on the observer conn so the same "SUT is gone" detector
	// works for every scenario. Probes use the OBSERVER connection
	// regardless of --connections / --nats-creds-dir so per-user creds
	// rotation can't cause permission-denied false-positives (F3 fix).
	var livenessFailed atomic.Bool
	var livenessReason atomic.Value
	var livenessWG sync.WaitGroup
	startLivenessWatcher(runCtx, rf, sc, deps, pool, metrics, p, runID, &livenessFailed, &livenessReason, &livenessWG, cancelRun)

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
	var drainTimedOut bool
	if publisher.asyncJS {
		select {
		case <-js.PublishAsyncComplete():
		case <-time.After(asyncDrainTimeout):
			drainTimedOut = true
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

	// Drain the pool's data connections (observer is drained by rt.Close).
	// This must happen BEFORE rt.Close so we don't double-drain the observer.
	for _, c := range pool.conns {
		if c != nil {
			_ = c.Drain()
		}
	}

	if genErr != nil {
		slog.Error("generator error", "error", genErr)
	}

	summary := buildSummary(&summaryInputs{
		rt:                rt,
		rf:                rf,
		p:                 p,
		siteID:            cfg.SiteID,
		runID:             runID,
		injectMode:        injectMode,
		warmupDeadline:    warmupDeadline,
		drainTimedOut:     drainTimedOut,
		settleOutcome:     settleOutcome,
		livenessFailed:    livenessFailed.Load(),
		missingReplies:    missingReplies,
		missingBroadcasts: missingBroadcasts,
		collector:         collector,
		samplers:          samplers,
		metrics:           metrics,
	})
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
	baseCode := exitCodeForFull(abortTripped.Load(), livenessFailed.Load(), summary.SentMeasured, totalErrs)
	// Exit code 4: UNTRUSTED verdict only when the base code would be 0
	// (clean pass). Higher-priority codes (1=clean-fail, 2=saturation,
	// 3=liveness) already indicate a problem and take precedence.
	if baseCode == 0 && summary.RunQualityVerdict.Verdict == "UNTRUSTED" {
		return 4, summary
	}
	return baseCode, summary
}

// ---------------------------------------------------------------------------
// Exit-code policy
// ---------------------------------------------------------------------------

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
//
// Note: exit code 4 (UNTRUSTED verdict) is applied by executeRun on top
// of this function when the base code would be 0 — see the verdict check
// after exitCodeForFull is called.
func exitCodeForFull(saturationAborted, livenessFailed bool, sent, totalErrs int) int {
	if livenessFailed {
		return 3
	}
	if saturationAborted {
		return 2
	}
	return DetermineExitCode(sent, totalErrs)
}

// ---------------------------------------------------------------------------
// Rate and window helpers
// ---------------------------------------------------------------------------

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
	// Read scenarios implement ReadinessProber in the registry. For those,
	// actual rate is the sum of post-warmup request counts across all kinds.
	// Messaging-pipeline does not implement ReadinessProber, so it falls
	// through to the E1/sentMeasured path below.
	if sc, ok := LookupScenario(scenario); ok {
		if _, isReadScenario := sc.(ReadinessProber); isReadScenario {
			// Read scenarios: count post-warmup requests across all kinds for
			// this scenario. RequestStats returns only "measured"-phase cells;
			// warmup samples are excluded by phase label, not by DiscardBefore.
			total := 0
			for _, rs := range requestStats {
				if rs.Scenario == scenario {
					total += rs.Count
				}
			}
			return float64(total) / secs
		}
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

// ---------------------------------------------------------------------------
// Shutdown and drain helpers
// ---------------------------------------------------------------------------

// asyncDrainTimeout caps the wait for in-flight async JetStream
// publishes to ack at shutdown. Same magnitude as drainTrailingReplies
// so the two shutdown phases are symmetric; both bounded so a wedged
// stream doesn't hang the run forever.
const asyncDrainTimeout = 5 * time.Second

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

// ---------------------------------------------------------------------------
// CSV export and consumer snapshots
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Subject helpers
// ---------------------------------------------------------------------------

func lastToken(subj string) string {
	i := strings.LastIndex(subj, ".")
	if i < 0 {
		return subj
	}
	return subj[i+1:]
}

// ---------------------------------------------------------------------------
// Scenario registry helpers
// ---------------------------------------------------------------------------

// scenarioNames returns a sorted slice of all registered scenario names.
// Used in error messages so the operator can see what's valid.
func scenarioNames() []string {
	all := AllScenarios()
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// buildLivenessProbeFromScenario returns a liveness probe function for the
// given scenario. Scenarios that implement LivenessProber provide their own
// probe; all others (including messaging-pipeline) fall back to a NATS
// round-trip-time check on the observer connection so the "SUT is gone"
// detector works for every scenario. F1 fix.
func buildLivenessProbeFromScenario(sc Scenario, deps ScenarioDeps, conn natsConnLike) func(context.Context) error {
	if lp, ok := sc.(LivenessProber); ok {
		return lp.BuildLivenessProbe(deps)
	}
	// Default fallback: NATS RTT check on the observer conn.
	return func(_ context.Context) error {
		if conn == nil {
			return nil
		}
		_, err := conn.RTT()
		if err != nil {
			return fmt.Errorf("liveness check (NATS RTT): %w", err)
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// runDeps — ScenarioDeps adapter
// ---------------------------------------------------------------------------

// runDeps implements ScenarioDeps using run-local state constructed inside
// executeRun. It is built after the publisher, requester, collector, and
// pool are ready, and passed to readiness probes, liveness probes,
// auto-warmup, and generator factories.
type runDeps struct {
	publisher   Publisher
	requester   Requester
	collector   *Collector
	metrics     *Metrics
	fixtures    *Fixtures
	preset      *Preset
	siteID      string
	maxInFlight int
	omission    *OmissionTracker
	injectMode  InjectMode
	// connIDFor maps userID → connection index; used by messaging-pipeline.
	connIDFor  func(userID string) string
	warmupPubl Publisher
	// msgIDs and warmupDeadline are set after the readiness + auto-warmup
	// phases complete, just before the generator factory is called.
	msgIDs         []string
	warmupDeadline time.Time
	// rt is the owning Runtime; used to forward Sites() and Subscribers().
	rt *Runtime
}

func (d *runDeps) Publisher() Publisher       { return d.publisher }
func (d *runDeps) Requester() Requester       { return d.requester }
func (d *runDeps) Collector() *Collector      { return d.collector }
func (d *runDeps) Metrics() *Metrics          { return d.metrics }
func (d *runDeps) Fixtures() *Fixtures        { return d.fixtures }
func (d *runDeps) Preset() *Preset            { return d.preset }
func (d *runDeps) SiteID() string             { return d.siteID }
func (d *runDeps) MaxInFlight() int           { return d.maxInFlight }
func (d *runDeps) WarmupPublisher() Publisher { return d.warmupPubl }
func (d *runDeps) Omission() *OmissionTracker { return d.omission }
func (d *runDeps) InjectMode() InjectMode     { return d.injectMode }
func (d *runDeps) WarmupDeadline() time.Time  { return d.warmupDeadline }
func (d *runDeps) MessageIDs() []string       { return d.msgIDs }
func (d *runDeps) Sites() []SiteDeps          { return d.rt.Sites() }
func (d *runDeps) Subscribers() *Subscribers  { return d.rt.Subscribers() }

// ConnIDFor maps a userID to the index of the data connection that
// publishes on its behalf. Used by the messaging-pipeline scenario.
// Not part of ScenarioDeps (messaging-pipeline-specific).
func (d *runDeps) ConnIDFor() func(userID string) string { return d.connIDFor }

// withRequester returns a shallow copy of d with the Requester field replaced.
// Used for the observer-routed liveness probe so it bypasses per-user creds
// rotation (F3 fix).
func (d *runDeps) withRequester(r Requester) *runDeps {
	cp := *d
	cp.requester = r
	return &cp
}
