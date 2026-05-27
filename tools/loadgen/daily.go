package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
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
	preset := fs.String("preset", "daily-heavy", "preset name (daily-light|daily-heavy|daily-power)")
	steps := fs.String("steps", "1000,2000,5000,10000,20000,50000,100000", "comma-separated N values")
	warmup := fs.Duration("warmup", 60*time.Second, "per-step warm-up")
	hold := fs.Duration("hold", 180*time.Second, "per-step hold")
	cooldown := fs.Duration("cooldown", 30*time.Second, "per-step cooldown")
	stopOnTrip := fs.Bool("stop-on-trip", true, "stop on first trip")
	maxDirect := fs.Int("max-direct-users", 20000, "direct-pool cap")
	mux := fs.Int("multiplex-pool-size", 200, "multiplex pool size")
	maxConns := fs.Int("max-conns-per-process", 25000, "safety ceiling on connections")
	csvPath := fs.String("csv", "", "optional CSV output path")
	if err := fs.Parse(args); err != nil {
		return dailyConfig{}, err
	}

	if _, ok := BuiltinPreset(*preset); !ok {
		return dailyConfig{}, fmt.Errorf("unknown preset %q", *preset)
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
//nolint:unused // wired up by runDaily in a later task
type stepEnv struct {
	collector      *Collector
	direct         *directPool
	multiplex      *multiplexPool
	users          []*userState
	thresholds     Thresholds
	pollPending    func(ctx context.Context) (map[string]int64, error)
	scrapeServices func(ctx context.Context) (map[string]int64, error)
	maxDirect      int // direct pool cap (from cfg.MaxDirectUsers)
	warmup         time.Duration
	hold           time.Duration
	cooldown       time.Duration
	mintJWT        func(ctx context.Context, account string) error // optional; nil = skip
}

// runStep executes one ramp step: activates additional users (delta over
// previous), warms up, holds, evaluates SLO signals, and cools down.
// The current step is `n`; the previous step's user count is `prevN` (0 for
// the first step). Users [prevN..n) are activated this step.
//
//nolint:unused // wired up by runDaily in a later task
func runStep(ctx context.Context, env *stepEnv, n, prevN int) StepResult {
	startedAt := time.Now()
	delta := n - prevN

	activateUsers(ctx, env, prevN, n)
	if delta > 0 {
		slog.Info("step warmup", "n", n, "delta", delta)
	}

	timer := time.NewTimer(env.warmup)
	select {
	case <-ctx.Done():
		timer.Stop()
		return StepResult{N: n, StartedAt: startedAt}
	case <-timer.C:
	}

	startPending, _ := env.pollPending(ctx)
	_, _ = env.scrapeServices(ctx) // first call records baseline

	env.collector.Reset()

	holdEnd := time.Now().Add(env.hold)
	for time.Now().Before(holdEnd) {
		select {
		case <-ctx.Done():
			return StepResult{N: n, StartedAt: startedAt}
		case <-time.After(5 * time.Second):
		}
	}

	endPending, _ := env.pollPending(ctx)
	svcErrors, _ := env.scrapeServices(ctx)

	in := stepInputs{
		N: n, StartedAt: startedAt, HoldDuration: env.hold,
		LatencySamples:  env.collector.LatencySamples(),
		AttemptedOps:    env.collector.AttemptedOps(),
		FailedOps:       env.collector.FailedOps(),
		ConsumerPending: diffPending(startPending, endPending),
		ServiceErrors:   svcErrors,
		Self:            snapshotSelfMetrics(),
	}
	r := evaluateStep(in, env.thresholds)

	select {
	case <-ctx.Done():
	case <-time.After(env.cooldown):
	}

	return r
}

// activateUsers brings users in the range [from, to) online: optionally
// mints a JWT, assigns them to a pool, opens connections / registers room
// interest. Rate-limited at 500 users/sec.
//
//nolint:unused // wired up by runDaily in a later task
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
		if env.direct != nil && env.direct.Size() < env.maxDirect {
			if err := env.direct.Add(u); err != nil {
				slog.Warn("direct pool add failed", "user", u.ID, "err", err)
				continue
			}
		} else if env.multiplex != nil {
			if err := env.multiplex.Add(u); err != nil {
				slog.Warn("multiplex pool add failed", "user", u.ID, "err", err)
				continue
			}
		}
	}
}
