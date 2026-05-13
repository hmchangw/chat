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
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsutil"
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
	// RunsDir is the root directory for artifact bundles. When non-empty,
	// Finalize writes runs/<run_id>/ with the full bundle. Empty disables
	// artifact writing (default — opt-in via RUNS_DIR env var).
	RunsDir string `env:"RUNS_DIR" envDefault:""`
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
		return runTeardown(ctx, cfg, os.Args[2:])
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
	runID := fs.String("run-id", "",
		"run ID for per-run credential storage; generated and printed if empty")
	withJWTs := fs.Bool("with-jwts", false,
		"mint placeholder JWTs for fixture users into runs/<run_id>/creds/ (Phase 3.8 will replace with real signed JWTs via auth-service admin RPC)")
	withFederation := fs.Bool("with-federation", false,
		"provision placeholder federation NKeys into runs/<run_id>/creds/ (Phase 3.9 will replace with real NKey generation)")
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

	// Credential provisioning is opt-in and only active when RUNS_DIR is set
	// (otherwise there's no artifact root to write under).
	if (*withJWTs || *withFederation) && cfg.RunsDir == "" {
		slog.Warn("--with-jwts/--with-federation require RUNS_DIR to be set; skipping credential provisioning")
		return 0
	}

	if *withJWTs || *withFederation {
		// Use the caller-supplied run ID or generate + print one.
		rid := *runID
		if rid == "" {
			rid = idgen.GenerateUUIDv7()
			fmt.Printf("run-id: %s\n", rid)
		}

		if *withJWTs {
			userNames := make([]string, len(fixtures.Users))
			for i := range fixtures.Users {
				userNames[i] = fixtures.Users[i].Account
			}
			if _, err := MintFixtureJWTs(ctx, cfg.RunsDir, rid, userNames); err != nil {
				slog.Error("mint fixture JWTs", "error", err)
				return 1
			}
		}

		if *withFederation {
			for _, peer := range []string{"site-a-peer", "site-b-peer"} {
				if _, err := ProvisionFederationNKey(ctx, cfg.RunsDir, rid, peer); err != nil {
					slog.Error("provision federation NKey", "peer", peer, "error", err)
					return 1
				}
			}
		}
	}

	return 0
}

func runTeardown(ctx context.Context, cfg *config, args []string) int {
	fs := flag.NewFlagSet("teardown", flag.ExitOnError)
	forceFlag := fs.Bool("force", false,
		"enumerate and drop all orphaned loadgen_* Mongo DBs and JetStream consumers")
	olderThan := fs.Duration("older-than", 0,
		"with --force, only drop runs whose lock row startedAt is older than this duration (0 = any orphan)")
	runID := fs.String("run-id", "",
		"with --force, target only this specific run ID")
	_ = fs.Parse(args)

	if *forceFlag {
		return dispatchTeardownForce(ctx, cfg, *olderThan, *runID)
	}

	// Non-force path: teardown is destructive — refuse unless the configured
	// DB carries the loadgen prefix. Override flag is intentionally not
	// exposed here; an operator who genuinely needs to drop a non-loadgen DB
	// can rename it temporarily or use mongosh directly.
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

	// Clean up the per-run creds directory when a run ID is known and RUNS_DIR
	// is configured. Force mode leaves creds alone (operators can rm -rf manually).
	if *runID != "" && cfg.RunsDir != "" {
		if err := CleanupCredsDir(cfg.RunsDir, *runID); err != nil {
			slog.Warn("cleanup creds dir", "error", err)
		}
	}

	slog.Info("teardown complete")
	return 0
}

// dispatchTeardownForce is the --force path of the teardown subcommand.
// It connects to Mongo and NATS, then delegates to runTeardownForce.
func dispatchTeardownForce(ctx context.Context, cfg *config, olderThan time.Duration, specificRunID string) int {
	mc, err := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if err != nil {
		slog.Error("mongo connect", "error", err)
		return 1
	}
	defer mongoutil.Disconnect(ctx, mc)

	nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
	if err != nil {
		slog.Error("nats connect", "error", err)
		return 1
	}
	defer func() { _ = nc.Drain() }()

	js, err := jetstream.New(nc.NatsConn())
	if err != nil {
		slog.Error("jetstream init", "error", err)
		return 1
	}

	rep, err := runTeardownForce(ctx, mc, js, teardownForceConfig{
		OlderThan:     olderThan,
		SpecificRunID: specificRunID,
	})
	if err != nil {
		slog.Error("teardown --force", "error", err)
		return 1
	}

	skipped := len(rep.SkippedDBs) + len(rep.SkippedConsumers)
	fmt.Printf("teardown --force: dropped %d Mongo DBs, %d JetStream consumers (skipped %d active)\n",
		len(rep.DroppedMongoDBs), len(rep.DroppedConsumers), skipped)
	return 0
}

// runRun is the entry point for the `run` subcommand. It validates all flags
// and config before opening any external connections, then delegates the run
// body to executeRun (see run.go).
//
// Exit codes from runRun (flag/parse path):
//
//	0  = --help
//	2  = flag/parse/config error
//
// Exit codes from executeRun (see exitCodeForFull in run.go):
//
//	0  = clean pass
//	1  = clean fail or startup error (e.g., NATS connect, JetStream init)
//	2  = saturation watcher fired (SUT got slow)
//	3  = liveness watcher fired (SUT became unreachable)
//	4  = clean pass but UNTRUSTED verdict (Phase 1a.6)
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

	// Connect to MongoDB. The lock lives in a SHARED database (SharedLockDBName)
	// so concurrent runs on different machines can see each other's lock rows.
	// Per-run data lives in a separate per-run DB; the two must NOT be confused.
	mongoClient, mongoErr := mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
	if mongoErr != nil {
		slog.Error("mongo connect (run lock)", "error", mongoErr)
		return 1
	}
	defer mongoutil.Disconnect(context.Background(), mongoClient)

	lockDB := mongoClient.Database(SharedLockDBName)
	runLock := NewRunLock(lockDB, cfg.NatsURL, rf.RunTTL)
	lp := &RunLockParams{
		Lock:            runLock,
		Scenario:        rf.Scenario,
		AllowConcurrent: rf.AllowConcurrent,
	}

	rt, err := NewRuntime(ctx, cfg, runID, lp)
	if err != nil {
		if errors.Is(err, ErrConcurrentRun) {
			fmt.Fprintln(os.Stderr, "error:", err)
			fmt.Fprintln(os.Stderr, "Wait for it to finish, or re-run with --allow-concurrent to bypass this check.")
			return 1
		}
		slog.Error("runtime init", "error", err)
		return 1
	}
	defer rt.Close() //nolint:errcheck

	exitCode, summary := executeRun(ctx, rt, &rf, &p, injectMode)

	if err := rt.Finalize(ctx, &summary); err != nil {
		slog.Error("finalize", "error", err)
	}
	return exitCode
}
