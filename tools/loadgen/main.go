package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/caarlos0/env/v11"
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
	// FederationSecondaryNATSURL is the NATS URL for site-b in the
	// federation-lag scenario. When non-empty, NewRuntime dials the secondary
	// and Sites() returns 2 SiteDeps. Typically set via
	// --federation-secondary-nats-url (mirrors the run flag).
	// Env: FEDERATION_SECONDARY_NATS_URL (optional, default disabled).
	FederationSecondaryNATSURL string `env:"FEDERATION_SECONDARY_NATS_URL" envDefault:""`
	// ToxiproxyURL is the toxiproxy admin endpoint for the chaos subcommand.
	// Env: TOXIPROXY_URL (optional, default http://localhost:8474).
	ToxiproxyURL string `env:"TOXIPROXY_URL" envDefault:"http://localhost:8474"`
	// Valkey backs the room-key store. Not marked required at the env level
	// because subcommands that don't touch the keystore (chaos, scenarios,
	// presets, recommend, doctor) must still parse a usable config. The seed
	// and teardown handlers check ValkeyAddr at the call site and exit 2 with
	// a clear message if the keystore is needed but not configured.
	ValkeyAddr     string `env:"VALKEY_ADDR"         envDefault:""`
	ValkeyPassword string `env:"VALKEY_PASSWORD"     envDefault:""`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: loadgen <seed|run|teardown|chaos|scenarios|presets|recommend|doctor> [flags]")
		os.Exit(2)
	}

	// Discoverability subcommands (Task X.1) require no external connections
	// and must work without NATS_URL / MONGO_URI env vars. Dispatch them
	// before the config parse so operators can explore the binary offline.
	switch os.Args[1] {
	case "scenarios":
		os.Exit(runScenarios(nil, os.Args[2:]))
	case "presets":
		os.Exit(runPresets(nil, os.Args[2:]))
	case "recommend":
		os.Exit(runRecommend(nil, os.Args[2:]))
	case "doctor":
		os.Exit(runDoctor(nil, os.Args[2:]))
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
	case "chaos":
		return runChaos(ctx, cfg, os.Args[2:])
	case "scenarios":
		return runScenarios(cfg, os.Args[2:])
	case "presets":
		return runPresets(cfg, os.Args[2:])
	case "recommend":
		return runRecommend(cfg, os.Args[2:])
	case "doctor":
		return runDoctor(cfg, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		return 2
	}
}
