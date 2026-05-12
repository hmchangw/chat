package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/caarlos0/env/v11"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/mongoutil"
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

// runRun is the entry point for the `run` subcommand. It validates all flags
// and config before opening any external connections, then delegates the run
// body to executeRun (see run.go).
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
	builtRamp, rerr := buildRamp(rf.Ramp.From, rf.Ramp.To, rf.Ramp.Duration, rf.Ramp.Shape)
	if rerr != nil {
		fmt.Fprintln(os.Stderr, rerr.Error())
		return 2
	}
	if err := validateRampVsRate(rf.Rate, builtRamp); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}

	runID := idgen.GenerateUUIDv7()
	rt, err := NewRuntime(ctx, cfg, runID)
	if err != nil {
		slog.Error("runtime init", "error", err)
		return 1
	}
	defer rt.Close() //nolint:errcheck

	exitCode := executeRun(ctx, rt, &rf, &p, injectMode)

	if err := rt.Finalize(ctx); err != nil {
		slog.Error("finalize", "error", err)
	}
	return exitCode
}
