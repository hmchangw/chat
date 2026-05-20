package main

// dispatch.go holds the subcommand handler functions that were extracted from
// main.go to keep main.go focused on wiring (config, signal handling,
// top-level dispatch). Each runXxx function is the entry point for one CLI
// subcommand and returns an OS exit code.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// errValkeyAddrUnset is returned by connectKeyStore when the operator did
// not set VALKEY_ADDR. Distinct from the generic dial error so the caller
// can print an actionable message instead of a stack-trace-like dial failure.
var errValkeyAddrUnset = errors.New(
	"VALKEY_ADDR is not set; loadgen seed/teardown need the room-key store " +
		"(set VALKEY_ADDR=host:port — broadcast-worker decrypts using the keys this seeds)")

// connectKeyStore opens the Valkey-backed room-key store used by seed and
// teardown. The grace period mirrors broadcast-worker's default; it does
// not affect loadgen's seed/teardown logic but keeps keystore behaviour
// uniform across processes.
//
// Returns errValkeyAddrUnset when cfg.ValkeyAddr is empty so callers can
// emit a clear "set VALKEY_ADDR" message rather than a low-level dial error.
func connectKeyStore(cfg *config) (roomkeystore.RoomKeyStore, error) {
	if cfg.ValkeyAddr == "" {
		return nil, errValkeyAddrUnset
	}
	return roomkeystore.NewValkeyStore(roomkeystore.Config{
		Addr:        cfg.ValkeyAddr,
		Password:    cfg.ValkeyPassword,
		GracePeriod: time.Hour,
	})
}

func runSeed(ctx context.Context, cfg *config, args []string) int {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	workload := fs.String("workload", "messages",
		"messages|members — which fixture shape to provision (members for members-sustained/members-capacity, messages otherwise)")
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
	includeChurn := fs.Bool("include-churn-fixtures", false,
		"subscription-churn scenario: provision a dedicated loadgen-churn- prefixed user/room pool alongside the main fixtures")
	includeFirstDM := fs.Bool("include-first-dm-fixtures", false,
		"first-dm scenario: provision a dedicated loadgen-firstdm- prefixed user pool")
	firstDMPairs := fs.Int("first-dm-pairs", 1000,
		"first-dm scenario: number of user pairs to provision (only effective with --include-first-dm-fixtures)")
	_ = fs.Parse(args)
	if *preset == "" {
		fmt.Fprintln(os.Stderr, "--preset required")
		return 2
	}
	if *workload == "members" {
		return runSeedMembers(ctx, cfg, *preset, *seed)
	}
	if *workload != "messages" {
		fmt.Fprintf(os.Stderr, "unknown workload: %s\n", *workload)
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
	keyStore, err := connectKeyStore(cfg)
	if err != nil {
		if errors.Is(err, errValkeyAddrUnset) {
			fmt.Fprintln(os.Stderr, err.Error())
			return 2
		}
		slog.Error("valkey connect", "error", err)
		return 1
	}
	defer func() { _ = keyStore.Close() }()
	db := client.Database(cfg.MongoDB)
	fixtures := BuildFixtures(&p, *seed, cfg.SiteID)
	if *includeChurn {
		fixtures = augmentWithChurnFixtures(&fixtures, &p, *seed)
	}
	if *includeFirstDM {
		augmentWithFirstDMFixtures(&fixtures, &p, *firstDMPairs)
	}
	if err := Seed(ctx, db, &fixtures); err != nil {
		slog.Error("seed", "error", err)
		return 1
	}
	if err := SeedRoomKeys(ctx, keyStore, fixtures.RoomKeys); err != nil {
		slog.Error("seed room keys", "error", err)
		return 1
	}
	slog.Info("seed complete",
		"preset", p.Name,
		"users", len(fixtures.Users),
		"rooms", len(fixtures.Rooms),
		"subs", len(fixtures.Subscriptions),
		"roomKeys", len(fixtures.RoomKeys))

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
			slog.Info("seed complete", "run_id", rid)
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
	workload := fs.String("workload", "messages",
		"messages|members — which fixture shape to tear down (mirrors the --workload used at seed time)")
	forceFlag := fs.Bool("force", false,
		"enumerate and drop all orphaned loadgen_* Mongo DBs and JetStream consumers")
	olderThan := fs.Duration("older-than", 0,
		"with --force, only drop runs whose lock row startedAt is older than this duration (0 = any orphan)")
	runID := fs.String("run-id", "",
		"with --force, target only this specific run ID")
	preset := fs.String("preset", "",
		"preset name (when set, also deletes the room keys seeded for that preset/seed pair)")
	seed := fs.Int64("seed", 42, "RNG seed (must match the seed used at seed time)")
	_ = fs.Parse(args)

	if *forceFlag {
		return dispatchTeardownForce(ctx, cfg, *olderThan, *runID)
	}

	if *workload == "members" {
		if *preset == "" {
			fmt.Fprintln(os.Stderr, "--preset required for --workload=members")
			return 2
		}
		return runTeardownMembers(ctx, cfg, *preset, *seed)
	}
	if *workload != "messages" {
		fmt.Fprintf(os.Stderr, "unknown workload: %s\n", *workload)
		return 2
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

	// When --preset is supplied, also drop the room keys seeded for that
	// preset/seed pair so Valkey doesn't accumulate orphaned keypairs across
	// runs. Skipped when --preset is empty so the simple "wipe Mongo" use
	// case stays cheap and doesn't require Valkey connectivity.
	if *preset != "" {
		p, ok := BuiltinPreset(*preset)
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown preset: %s\n", *preset)
			return 2
		}
		keyStore, err := connectKeyStore(cfg)
		if err != nil {
			if errors.Is(err, errValkeyAddrUnset) {
				fmt.Fprintln(os.Stderr, err.Error())
				return 2
			}
			slog.Error("valkey connect", "error", err)
			return 1
		}
		defer func() { _ = keyStore.Close() }()
		fixtures := BuildFixtures(&p, *seed, cfg.SiteID)
		roomIDs := make([]string, len(fixtures.Rooms))
		for i := range fixtures.Rooms {
			roomIDs[i] = fixtures.Rooms[i].ID
		}
		if err := TeardownRoomKeys(ctx, keyStore, roomIDs); err != nil {
			slog.Error("teardown room keys", "error", err)
			return 1
		}
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

	slog.Info("teardown --force complete",
		"dropped_dbs", len(rep.DroppedMongoDBs),
		"dropped_consumers", len(rep.DroppedConsumers),
		"skipped", len(rep.SkippedDBs)+len(rep.SkippedConsumers))
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

	// Validate ramp vs rate BEFORE opening any external connections.
	// The ramp itself was already built and validated in ParseRunFlags;
	// rf.BuiltRamp is nil when no ramp was requested.
	if err := validateRampVsRate(rf.Rate, rf.BuiltRamp); err != nil {
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

	// Propagate the --federation-secondary-nats-url flag into cfg so
	// NewRuntime can dial site-b when the flag is set. The flag takes
	// precedence over the FEDERATION_SECONDARY_NATS_URL env var.
	if rf.FederationSecondaryNATSURL != "" {
		cfg.FederationSecondaryNATSURL = rf.FederationSecondaryNATSURL
	}
	// Same precedence rule for --max-in-flight: only the >0 value overrides
	// the env default. NewRuntime reads cfg.MaxInFlight when building runDeps.
	if rf.MaxInFlight > 0 {
		cfg.MaxInFlight = rf.MaxInFlight
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
