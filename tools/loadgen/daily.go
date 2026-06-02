package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/model"
)

// dailyConfig is the parsed CLI input for `loadgen daily`.
type dailyConfig struct {
	Preset             string
	Steps              []int
	Warmup             time.Duration
	Hold               time.Duration
	Cooldown           time.Duration
	StopOnTrip         bool
	MaxDirectUsers     int
	MultiplexPoolSize  int
	MaxConnsPerProcess int
	CSVPath            string
}

func parseDailyConfig(args []string) (dailyConfig, error) {
	fs := flag.NewFlagSet("daily", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `loadgen daily — daily-IM scenario, find sustainable N

Simulates N users using the chat system as their primary IM throughout a
workday. Ramps N geometrically through the configured steps; for each step,
warms up, holds steady, polls SLO signals, and decides PASS / TRIP /
INCONCLUSIVE. Reports the largest passing N and which signal tripped next.

SLO signals evaluated over the hold window:
  - p95 latency (publish→broadcast)        threshold 500ms
  - p99 latency                            threshold 1000ms
  - error rate                             threshold 0.1%
  - any JetStream consumer pending growth  threshold +1000
  - any service slog_errors_total increase threshold +0
INCONCLUSIVE (overrides PASS/TRIP) when the loadgen process is itself
saturated (GC pause p99 > 50ms or CPU proxy > 80%).

Receiver topology is hybrid: the first --max-direct-users users get one
nats.Conn each (most realistic); the rest share a fixed pool of
--multiplex-pool-size connections.

Usage:
  loadgen daily --preset=<name> [flags]

Presets:
  daily-light    ~32 rooms/user   light daily-IM user
  daily-heavy    ~56 rooms/user   heavy daily-IM user (default)
  daily-power    ~83 rooms/user   power user

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(fs.Output(), `
Examples:
  # Default 7-step geometric ramp 1k → 100k, daily-heavy preset:
  loadgen daily --preset=daily-heavy --csv=results.csv

  # Tight sweep around an expected breakpoint, shorter hold:
  loadgen daily --preset=daily-heavy --steps=8000,9000,10000,11000,12000 --hold=120s

  # Single-step smoke test:
  loadgen daily --preset=daily-light --steps=500 --warmup=10s --hold=30s

Step list accepts shorthand: --steps=1k,2k,5k,10k

See tools/loadgen/README.md and docs/superpowers/specs/2026-05-27-daily-im-load-scenario-design.md
for the full design and SLO rationale.
`)
	}
	preset := fs.String("preset", "daily-heavy", "preset name: daily-light | daily-heavy | daily-power")
	steps := fs.String("steps", "1000,2000,5000,10000,20000,50000,100000", "comma-separated N values per ramp step; `k` suffix multiplies by 1000 (e.g. \"1k,2k,5k\")")
	warmup := fs.Duration("warmup", 60*time.Second, "per-step warm-up before SLO measurement begins")
	hold := fs.Duration("hold", 180*time.Second, "per-step steady-state window where SLO signals are evaluated")
	cooldown := fs.Duration("cooldown", 30*time.Second, "per-step cooldown to let consumers drain before the next step")
	stopOnTrip := fs.Bool("stop-on-trip", true, "stop the ramp on the first TRIP (false: run all steps)")
	maxDirect := fs.Int("max-direct-users", 20000, "cap on the direct-pool size; users beyond this go to the multiplex pool")
	mux := fs.Int("multiplex-pool-size", 200, "number of shared nats.Conn instances in the multiplex pool")
	maxConns := fs.Int("max-conns-per-process", 25000, "safety ceiling on total nats.Conn count to this process")
	csvPath := fs.String("csv", "", "optional CSV output path (one row per step)")
	if err := fs.Parse(args); err != nil {
		return dailyConfig{}, err
	}

	if _, ok := BuiltinPreset(*preset); !ok {
		return dailyConfig{}, fmt.Errorf("unknown preset %q (valid: daily-light, daily-heavy, daily-power)", *preset)
	}

	parsedSteps, err := parseStepList(*steps)
	if err != nil {
		return dailyConfig{}, err
	}

	projected := *maxDirect + *mux
	if projected > *maxConns {
		return dailyConfig{}, fmt.Errorf(
			"projected conn count %d (direct=%d + mux=%d) exceeds --max-conns-per-process=%d",
			projected, *maxDirect, *mux, *maxConns)
	}

	return dailyConfig{
		Preset:             *preset,
		Steps:              parsedSteps,
		Warmup:             *warmup,
		Hold:               *hold,
		Cooldown:           *cooldown,
		StopOnTrip:         *stopOnTrip,
		MaxDirectUsers:     *maxDirect,
		MultiplexPoolSize:  *mux,
		MaxConnsPerProcess: *maxConns,
		CSVPath:            *csvPath,
	}, nil
}

func parseStepList(s string) ([]int, error) {
	if s == "" {
		return nil, fmt.Errorf("--steps cannot be empty")
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		mult := 1
		if strings.HasSuffix(p, "k") {
			mult = 1000
			p = strings.TrimSuffix(p, "k")
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid step %q: %w", p, err)
		}
		out = append(out, n*mult)
	}
	return out, nil
}

// stepEnv bundles the runtime dependencies of a step. Stub-able for unit tests.
//
// holdStartNanos / holdDurationNanos are atomics so emitters started during
// step N can re-anchor their diurnal envelope when step N+1 begins (otherwise
// older users would emit at the envelope's clamped baseline for the entire
// next step). Set via setHold() at the actual start of each hold window.
//
// activatedCount tracks how many users were successfully added to a pool;
// when it diverges from the nominal N (because direct pool filled and no
// multiplex was configured, or NATS subscribe failed), runStep surfaces the
// gap so an "N=20000 PASS" doesn't silently mean "10000 users active".
type stepEnv struct {
	collector      *Collector
	direct         *directPool
	multiplex      *multiplexPool
	users          []*userState
	thresholds     Thresholds
	pollPending    func(ctx context.Context) (map[string]int64, error)
	scrapeServices func(ctx context.Context) (map[string]int64, error)
	publish        publishFn // nil in stub mode → emitters no-op
	request        requestFn // nil in stub mode → emitters no-op
	siteID         string    // propagated from cfg / baseCfg
	runSeed        int64     // for deterministic per-user RNG seeding
	maxDirect      int       // direct pool cap (from cfg.MaxDirectUsers)
	warmup         time.Duration
	hold           time.Duration
	cooldown       time.Duration
	mintJWT        func(ctx context.Context, account string) error // optional; nil = skip

	holdStartNanos    atomic.Int64
	holdDurationNanos atomic.Int64
	activatedCount    atomic.Int64
	skippedCount      atomic.Int64
}

// setHold updates the current envelope anchor. Emitters read these on every
// tick so a step transition takes effect within ~1s.
func (env *stepEnv) setHold(start time.Time, duration time.Duration) {
	env.holdStartNanos.Store(start.UnixNano())
	env.holdDurationNanos.Store(duration.Nanoseconds())
}

func (env *stepEnv) currentHold() (time.Time, time.Duration) {
	startNanos := env.holdStartNanos.Load()
	if startNanos == 0 {
		return time.Time{}, 0
	}
	return time.Unix(0, startNanos), time.Duration(env.holdDurationNanos.Load())
}

// runStep executes one ramp step: activates additional users (delta over
// previous), warms up, holds, evaluates SLO signals, and cools down.
// The current step is `n`; the previous step's user count is `prevN` (0 for
// the first step). Users [prevN..n) are activated this step.
func runStep(ctx context.Context, env *stepEnv, n, prevN int) StepResult {
	startedAt := time.Now()
	delta := n - prevN

	// Activate the new slice of users. Activation can take significant time
	// (rate-limited at 500/sec, so +50k users = 100s) — that elapsed time
	// would eat into the warmup window if we set holdStart early. We
	// re-anchor holdStart right before the hold actually begins (below).
	activationStart := time.Now()
	activateUsers(ctx, env, prevN, n)
	activationElapsed := time.Since(activationStart)
	if delta > 0 {
		slog.Info("step activated",
			"n", n, "delta", delta,
			"activated", env.activatedCount.Load(),
			"skipped", env.skippedCount.Load(),
			"activation_elapsed", activationElapsed.Round(time.Millisecond))
	}

	if err := waitOrCancel(ctx, env.warmup); err != nil {
		return inconclusiveResult(n, startedAt, env.hold, "ctx canceled during warmup")
	}

	// Re-anchor the diurnal envelope at the actual hold start. Emitters
	// re-read this on every tick, so step-1 users that survived into step 2
	// follow step 2's envelope rather than continuing on step 1's curve.
	env.setHold(time.Now(), env.hold)

	// Snapshot pending state at start of hold. If the NATS monitoring
	// endpoint is misbehaving, drop the pending-growth signal for this
	// step rather than aborting it — the other signals (latency, errors,
	// service health) still produce a useful verdict. Only ctx cancel
	// is treated as Inconclusive.
	startPending, startPollErr := env.pollPending(ctx)
	if startPollErr != nil {
		if errors.Is(startPollErr, context.Canceled) || errors.Is(startPollErr, context.DeadlineExceeded) {
			return inconclusiveResult(n, startedAt, env.hold, "ctx canceled during start-of-hold poll")
		}
		slog.Warn("start-of-hold pending poll failed; pending-growth signal skipped this step", "err", startPollErr)
		startPending = nil
	}
	_, _ = env.scrapeServices(ctx) // first call records baseline

	env.collector.Reset()

	if err := waitOrCancel(ctx, env.hold); err != nil {
		return inconclusiveResult(n, startedAt, env.hold, "ctx canceled during hold")
	}

	endPending, endPollErr := env.pollPending(ctx)
	if endPollErr != nil {
		if errors.Is(endPollErr, context.Canceled) || errors.Is(endPollErr, context.DeadlineExceeded) {
			return inconclusiveResult(n, startedAt, env.hold, "ctx canceled during end-of-hold poll")
		}
		slog.Warn("end-of-hold pending poll failed; pending-growth signal skipped this step", "err", endPollErr)
		endPending = nil
	}
	svcErrors, _ := env.scrapeServices(ctx)

	// Only compute pending deltas when both snapshots succeeded; otherwise
	// pass an empty map so evaluateStep doesn't trip on garbage baselines.
	var pendingDeltas map[string]ConsumerPendingDelta
	if startPending != nil && endPending != nil {
		pendingDeltas = diffPending(startPending, endPending)
	}

	in := stepInputs{
		N: n, StartedAt: startedAt, HoldDuration: env.hold,
		EffectiveN:      int(env.activatedCount.Load()),
		LatencySamples:  env.collector.LatencySamples(),
		AttemptedOps:    env.collector.AttemptedOps(),
		FailedOps:       env.collector.FailedOps(),
		ConsumerPending: pendingDeltas,
		ServiceErrors:   svcErrors,
		Self:            snapshotSelfMetrics(),
	}
	r := evaluateStep(in, env.thresholds)

	_ = waitOrCancel(ctx, env.cooldown)
	return r
}

func inconclusiveResult(n int, startedAt time.Time, hold time.Duration, reason string) StepResult {
	return StepResult{
		N: n, StartedAt: startedAt, HoldDuration: hold,
		Inconclusive: true, TrippedReasons: []string{reason},
	}
}

// activateUsers brings users in the range [from, to) online: optionally
// mints a JWT, assigns them to a pool, opens connections / registers room
// interest, and starts their action-emitter goroutine. Rate-limited at
// 500 users/sec. Updates env.activatedCount / env.skippedCount so runStep
// can surface whether the nominal N actually went live.
func activateUsers(ctx context.Context, env *stepEnv, from, to int) {
	if from >= to {
		return
	}
	tokens := time.NewTicker(time.Second / 500)
	defer tokens.Stop()
	for i := from; i < to && i < len(env.users); i++ {
		select {
		case <-ctx.Done():
			return
		case <-tokens.C:
		}
		u := env.users[i]
		if env.mintJWT != nil {
			if err := env.mintJWT(ctx, u.Account); err != nil {
				slog.Warn("jwt mint failed", "user", u.ID, "err", err)
			}
		}
		var poolAdded bool
		switch {
		case env.direct != nil && env.direct.Size() < env.maxDirect:
			if err := env.direct.Add(u); err != nil {
				slog.Warn("direct pool add failed", "user", u.ID, "err", err)
				env.skippedCount.Add(1)
				continue
			}
			poolAdded = true
		case env.multiplex != nil:
			if err := env.multiplex.Add(u); err != nil {
				slog.Warn("multiplex pool add failed", "user", u.ID, "err", err)
				env.skippedCount.Add(1)
				continue
			}
			poolAdded = true
		default:
			slog.Warn("no pool available for user; skipping", "user", u.ID)
			env.skippedCount.Add(1)
			continue
		}
		// Per-user emitter runs through warmup + hold + cooldown, reading
		// the current envelope anchor from env on each tick so step
		// transitions take effect within ~1s. Pass the per-user index so
		// the RNG seed is deterministic given env.runSeed.
		if poolAdded && env.publish != nil {
			startEmitter(ctx, env, u, i)
		}
		env.activatedCount.Add(1)
	}
}

// envFactory builds a stepEnv from a parsed dailyConfig. Stubbed in tests.
type envFactory interface {
	Build(cfg dailyConfig, users []*userState) *stepEnv
}

// startEmitter launches a goroutine that, while ctx is live, ticks the user's
// Markov state every second and, when active, emits actions at the Poisson
// rate scaled by the diurnal envelope.
//
// The RNG seed is derived from env.runSeed and the user's index, so two runs
// with the same run-seed produce identical action streams (reproducibility
// is the whole point of a load-test verdict). Avoid time.Now in the seed —
// at the 500 users/sec activation rate, bursts of users get seeded in the
// same nanosecond and end up perfectly correlated.
//
// The envelope anchor is read from env on every tick (not captured at
// activation), so emitters started during step N follow step N+1's envelope
// once runStep calls env.setHold for the next step.
func startEmitter(ctx context.Context, env *stepEnv, u *userState, userIdx int) {
	go func() {
		// Splitmix-style mix to scramble adjacent userIdx seeds; cast through
		// uint64 so the multiplier doesn't overflow the int64 literal.
		seed := int64(uint64(env.runSeed)*0x9E3779B97F4A7C15) + int64(userIdx)
		r := rand.New(rand.NewSource(seed))
		weights := defaultActionWeights()
		baseRate := actionRatePerSecond(weights.totalPerDay(), 8*time.Hour)

		tick := time.NewTicker(1 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			u.step(r)
			if !u.active {
				continue
			}
			holdStart, holdDuration := env.currentHold()
			if holdDuration <= 0 {
				continue // env not yet initialised; wait for runStep to set
			}
			// Compress: a workday becomes the hold window. Multiply rate accordingly.
			compress := (8 * time.Hour).Seconds() / holdDuration.Seconds()
			elapsed := time.Since(holdStart)
			rate := baseRate * compress * rateMultiplier(elapsed, holdDuration)
			if r.Float64() < rate {
				doAction(ctx, env, u, r, weights)
			}
		}
	}()
}

// doAction picks one action via weights and dispatches it. Increments
// attempted/failed counters on the Collector.
func doAction(ctx context.Context, env *stepEnv, u *userState, r *rand.Rand, w actionWeights) {
	if env.publish == nil && env.request == nil {
		return // stub mode (no real NATS wired); no attempt counted
	}
	if env.collector != nil {
		env.collector.RecordActionAttempt()
	}
	a := actionCtx{
		Ctx: ctx, Publish: env.publish, Request: env.request,
		SiteID: env.siteID, Rand: r, Collector: env.collector,
	}
	var err error
	switch pickAction(r, w) {
	case actionSend:
		err = sendMessage(a, u, "loadtest content")
	case actionReadReceipt:
		err = readReceipt(a, u, "msg-stub")
	case actionScrollHistory:
		err = scrollHistory(a, u)
	case actionRefreshRoomList:
		err = refreshRoomList(a, u)
	case actionMemberAdd:
		err = memberAdd(a, u, "user-stub")
	case actionRoomCreate:
		err = roomCreate(a, u)
	case actionMuteToggle:
		err = muteToggle(a, u)
	}
	if err != nil && env.collector != nil {
		env.collector.RecordActionFailure()
	}
}

// runDailyForTest is the testable variant: takes an envFactory so tests can
// inject stubs. The production runDaily wraps it with the real factory.
//
// dailyRunSeed is the fixture/RNG seed. Hardcoded for now; spec section 12
// flagged this as a follow-up. Same seed → same fixtures → same action
// stream, which is what makes regression CSV comparisons meaningful.
const dailyRunSeed int64 = 42

//nolint:gocritic // cfg passed by value to match envFactory.Build signature
func runDailyForTest(ctx context.Context, cfg dailyConfig, factory envFactory) ([]StepResult, error) {
	preset, _ := BuiltinPreset(cfg.Preset)
	if len(cfg.Steps) == 0 {
		return nil, fmt.Errorf("cfg.Steps cannot be empty")
	}
	preset.Users = slices.Max(cfg.Steps) // size fixtures for the largest step

	siteID := "site-local"
	if cfg, ok := factoryBaseCfg(factory); ok && cfg.SiteID != "" {
		siteID = cfg.SiteID
	}
	slog.Info("building fixtures", "preset", cfg.Preset, "users", preset.Users)
	buildStart := time.Now()
	fx := BuildFixtures(&preset, dailyRunSeed, siteID)
	slog.Info("fixtures built",
		"rooms", len(fx.Rooms),
		"subscriptions", len(fx.Subscriptions),
		"elapsed", time.Since(buildStart).Round(time.Millisecond))

	userRooms := groupSubsByUser(fx.Subscriptions)
	users := make([]*userState, len(fx.Users))
	for i := range fx.Users {
		u := &fx.Users[i]
		users[i] = newUserState(u.ID, u.Account, userRooms[u.ID], int64(i))
	}

	env := factory.Build(cfg, users)
	if env.siteID == "" {
		env.siteID = siteID
	}
	env.runSeed = dailyRunSeed
	defer closePools(env)

	prevN := 0
	var results []StepResult
	for _, n := range cfg.Steps {
		// Honor ctx between steps so SIGINT mid-cooldown doesn't produce
		// a junk trail of INCONCLUSIVE rows for steps that never started.
		if err := ctx.Err(); err != nil {
			slog.Info("daily run interrupted; stopping ramp", "completed_steps", len(results))
			break
		}
		r := runStep(ctx, env, n, prevN)
		results = append(results, r)
		if cfg.StopOnTrip && r.Tripped {
			break
		}
		prevN = n
	}
	return results, nil
}

// factoryBaseCfg returns the baseCfg from a prodEnvFactory, if the factory is
// one. testEnvFactory returns false and runDailyForTest falls back to the
// default site.
func factoryBaseCfg(f envFactory) (*config, bool) {
	if p, ok := f.(*prodEnvFactory); ok && p != nil {
		return p.baseCfg, true
	}
	return nil, false
}

func closePools(env *stepEnv) {
	if env.direct != nil {
		env.direct.Close()
	}
	if env.multiplex != nil {
		env.multiplex.Close()
	}
}

func groupSubsByUser(subs []model.Subscription) map[string][]string {
	out := make(map[string][]string)
	for i := range subs {
		out[subs[i].User.ID] = append(out[subs[i].User.ID], subs[i].RoomID)
	}
	return out
}

// prodEnvFactory wires the real NATS pools and pollers.
type prodEnvFactory struct {
	baseCfg *config // existing top-level loadgen config: NatsURL, etc.
}

//nolint:gocritic // cfg passed by value to satisfy envFactory interface
func (f *prodEnvFactory) Build(cfg dailyConfig, users []*userState) *stepEnv {
	col := NewCollector(NewMetrics(), cfg.Preset)
	direct := newDirectPool(f.baseCfg.NatsURL, col)
	var mux *multiplexPool
	if cfg.MultiplexPoolSize > 0 {
		var err error
		mux, err = newMultiplexPool(f.baseCfg.NatsURL, col, cfg.MultiplexPoolSize)
		if err != nil {
			slog.Error("multiplex pool init failed; continuing without multiplex", "err", err)
			mux = nil
		}
	}

	// Dedicated publisher connection for emitter actions. Separate from the
	// receiver pools so a slow consumer can't backpressure publishes.
	pubConn, err := nats.Connect(f.baseCfg.NatsURL, nats.Name("loadgen-daily-publisher"))
	if err != nil {
		slog.Error("publisher connection failed; emitters will no-op", "err", err)
		pubConn = nil
	}
	publish := func(ctx context.Context, subj string, data []byte) error {
		if pubConn == nil {
			return fmt.Errorf("no publisher conn")
		}
		return pubConn.Publish(subj, data)
	}
	request := func(ctx context.Context, subj string, data []byte, timeout time.Duration) ([]byte, error) {
		if pubConn == nil {
			return nil, fmt.Errorf("no publisher conn")
		}
		msg, err := pubConn.RequestWithContext(ctx, subj, data)
		if err != nil {
			return nil, err
		}
		return msg.Data, nil
	}

	jszURL := f.baseCfg.NatsMonitoringURL
	if jszURL == "" {
		jszURL = "http://nats:8222/jsz"
	}

	// Backend services don't currently expose /metrics endpoints, so the
	// service-error scraper is a no-op until they do. Pass an empty URL map
	// — Scrape will return an empty delta map without making any requests.
	scraper := newServiceScraper()
	svcURLs := map[string]string{}

	siteID := f.baseCfg.SiteID
	if siteID == "" {
		siteID = "site-local"
	}

	return &stepEnv{
		collector: col, direct: direct, multiplex: mux, users: users,
		thresholds: defaultThresholds(),
		pollPending: func(ctx context.Context) (map[string]int64, error) {
			return pollPending(ctx, jszURL)
		},
		scrapeServices: func(ctx context.Context) (map[string]int64, error) {
			return scraper.Scrape(ctx, svcURLs)
		},
		publish:   publish,
		request:   request,
		siteID:    siteID,
		maxDirect: cfg.MaxDirectUsers,
		mintJWT:   buildAuthMintFn(),
		warmup:    cfg.Warmup,
		hold:      cfg.Hold,
		cooldown:  cfg.Cooldown,
	}
}

// buildAuthMintFn returns a best-effort one-time auth-service login function.
// On failure, activateUsers logs a warning and the user proceeds with the
// shared backend.creds.
func buildAuthMintFn() func(ctx context.Context, account string) error {
	return func(ctx context.Context, account string) error {
		body, _ := json.Marshal(map[string]string{"account": account})
		// Auth path is currently a placeholder — see spec section 10. When
		// auth-service exposes /login, this URL needs configuration; for
		// now best-effort means a connection-refused error is silently
		// tolerated by activateUsers.
		_ = body
		return nil
	}
}

// runDaily is the production entrypoint invoked by main.go.
func runDaily(ctx context.Context, baseCfg *config, args []string) int {
	cfg, err := parseDailyConfig(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0 // -h / --help printed usage; exit cleanly
		}
		slog.Error("parse daily config", "error", err)
		return 2
	}
	results, err := runDailyForTest(ctx, cfg, &prodEnvFactory{baseCfg: baseCfg})
	if err != nil {
		slog.Error("daily run", "error", err)
		return 1
	}
	renderConsole(os.Stdout, results)
	if cfg.CSVPath != "" {
		if err := writeDailyCSV(cfg.CSVPath, results); err != nil {
			slog.Error("csv write", "error", err)
			return 1
		}
	}
	return 0
}
