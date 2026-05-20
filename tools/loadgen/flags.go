package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// LoadgenDBPrefix is the required prefix on MONGO_DB for state-mutating
// loadgen subcommands (seed, teardown). Refusing to operate on databases
// that don't carry this prefix avoids the footgun where an operator
// accidentally seeds (or worse, tears down) the production "chat" DB.
const LoadgenDBPrefix = "loadgen"

// ErrMongoDBNotIsolated is returned by guardMongoDB when the configured
// MONGO_DB doesn't begin with LoadgenDBPrefix.
var ErrMongoDBNotIsolated = errors.New(
	"refusing to operate on Mongo DB without 'loadgen' prefix; set MONGO_DB=loadgen[suffix] " +
		"or pass --i-know-what-i-am-doing")

// guardMongoDB enforces the isolation rule: any DB name not starting
// with LoadgenDBPrefix is refused unless the caller has explicitly
// opted out. The "opt out" path exists for one-off operator workflows
// that genuinely need to touch a shared DB (recoveries, migrations);
// it is NOT meant to be hard-coded by other entry points.
func guardMongoDB(dbName string, override bool) error {
	if override {
		return nil
	}
	if !strings.HasPrefix(dbName, LoadgenDBPrefix) {
		return fmt.Errorf("%w: got MONGO_DB=%q", ErrMongoDBNotIsolated, dbName)
	}
	return nil
}

// (Inject-mode parsing lives in generator.go as the exported
// `ParseInjectMode`; previously this file had a duplicate `parseInjectMode`
// that was consolidated when PR #203 added the exported helper.)

// parseScenarioFlag validates the --scenario string against the registry.
// Returns nil when valid.
func parseScenarioFlag(s string) error {
	if _, ok := LookupScenario(s); ok {
		return nil
	}
	return fmt.Errorf("unknown scenario %q; run 'loadgen scenarios' to list registered scenarios", s)
}

// ErrMissingRampFields is returned when only some --ramp-* fields are
// set. Either all three of from/to/duration are positive, or all three
// are zero (no ramp). Mixed input is a config error.
var ErrMissingRampFields = errors.New("--ramp-from, --ramp-to, --ramp-duration must all be > 0 when ramping")

// parseRampShape maps the --ramp-shape string to its enum.
func parseRampShape(s string) (RampShape, error) {
	switch s {
	case "linear":
		return RampLinear, nil
	case "exponential":
		return RampExponential, nil
	default:
		return 0, fmt.Errorf("unknown ramp shape: %s (want linear|exponential)", s)
	}
}

// buildRamp constructs a *Ramp from the four --ramp-* flag values.
// Returns (nil, nil) when no ramp is requested (all three numeric
// fields are 0). Returns an error if the user set only some fields
// or supplied an invalid shape.
func buildRamp(from, to int, dur time.Duration, shape string) (*Ramp, error) {
	if from <= 0 && to <= 0 && dur <= 0 {
		return nil, nil // no ramp configured
	}
	if from <= 0 || to <= 0 || dur <= 0 {
		return nil, ErrMissingRampFields
	}
	rs, err := parseRampShape(shape)
	if err != nil {
		return nil, err
	}
	return &Ramp{From: from, To: to, Duration: dur, Shape: rs}, nil
}

// runFlags holds all parsed CLI flags for the "run" subcommand. It is
// populated by ParseRunFlags and consumed by runRun.
type runFlags struct {
	Scenario        string
	Preset          string
	Seed            int64
	Rate            int
	Duration        time.Duration
	Warmup          time.Duration
	Inject          string
	RequestTimeout  time.Duration
	CSV             string
	NATSCredsDir    string
	AllowConcurrent bool
	// MaxInFlight, when > 0, overrides the MAX_IN_FLIGHT env var for this
	// run. Zero or negative means "use the env default". Exposed as a flag
	// so operators can sweep the concurrency cap without touching the
	// container's environment.
	MaxInFlight int
	RunTTL      time.Duration
	Abort       abortFlags
	Ramp        rampFlags
	// BuiltRamp is the pre-validated *Ramp built from Ramp fields by ParseRunFlags.
	// Nil means no ramp is configured. executeRun reads this directly instead of
	// rebuilding the Ramp from Ramp.From/To/Duration/Shape (Phase 2 pre-condition).
	BuiltRamp           *Ramp
	AutoWarmup          autoWarmupFlags
	Liveness            livenessFlags
	Readiness           readinessFlags
	Progress            progressFlags
	Conn                connFlags
	JS                  jetStreamFlags
	Settle              SettleFlags
	RAW                 RAWFlags
	SearchSync          SearchSyncFlags
	ReceiptCoverage     float64
	MutateRate          int
	EditAgeDistribution string
	ChurnRate           int
	FirstDMRecycle      bool
	AuthStormPeriod     time.Duration
	// AuthURL overrides the auth-service base URL for the auth-load scenario.
	AuthURL string
	// Federation flags (Phase 3 §3.9).
	FederationFlap             bool
	FederationCrossRead        bool
	FlapPeriod                 time.Duration
	FlapDown                   time.Duration
	FederationSecondaryNATSURL string
}

type abortFlags struct {
	P99Ms               int
	P99Sustain          time.Duration
	ErrorPct            float64
	ErrorSustain        time.Duration
	WindowMaxSamples    int
	WindowMaxSamplesSet bool // true when --abort-window-max-samples was explicitly passed
}

type rampFlags struct {
	From     int
	To       int
	Duration time.Duration
	Shape    string
}

type autoWarmupFlags struct {
	Enabled bool
	Rate    int
}

type livenessFlags struct {
	Interval time.Duration
	Failures int
	Timeout  time.Duration
}

type readinessFlags struct {
	Skip    bool
	Timeout time.Duration
}

type progressFlags struct {
	Interval time.Duration
}

type connFlags struct {
	Connections int
}

type jetStreamFlags struct {
	AsyncMaxPending int
}

// RAWFlags holds flags specific to the raw-consistency scenario.
type RAWFlags struct {
	// PollInterval is the interval between visibility polls per read path.
	PollInterval time.Duration
	// Timeout is the per-message deadline before declaring not-visible.
	Timeout time.Duration
}

// SearchSyncFlags holds flags specific to the search-sync-lag scenario.
// Kept distinct from RAWFlags so the two scenarios don't share defaults —
// raw-consistency's 10ms/5s pair is way out of scale for an ES refresh of 30s.
type SearchSyncFlags struct {
	// PollInterval is the interval between search.messages visibility polls.
	PollInterval time.Duration
	// Timeout is the per-message poll deadline before declaring not-visible.
	Timeout time.Duration
	// ACLWait is the time to wait after the one-shot user-room ACL bootstrap
	// publishes before the per-tick publish/poll loop starts; covers ES
	// refresh_interval plus bulk-flush slack.
	ACLWait time.Duration
	// SkipACLBootstrap skips the one-shot ACL bootstrap + 35s wait at Run
	// start. Set when running multiple back-to-back search-sync-lag runs
	// against a warm cluster where the user-room ACL doc is already
	// populated from a prior seed/run. Saves 35s per iteration during
	// fast-feedback tuning loops. Operators should leave this off for
	// the first run after a fresh `loadgen seed`.
	SkipACLBootstrap bool
}

// PrintRunHelp writes the run-subcommand flag usage to w in the same
// format that the standard flag package uses for ExitOnError FlagSets:
//
//	Usage of run:
//	  -flag-name type
//	    	description (default value)
//
// This is called by runRun when ParseRunFlags returns flag.ErrHelp so that
// `loadgen run --help` produces the canonical flag listing.
func PrintRunHelp(w io.Writer) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(w)
	var rf runFlags
	rf.registerOn(fs)
	fmt.Fprintf(w, "Usage of run:\n")
	fs.PrintDefaults()
}

// ParseRunFlags parses the given argument slice into a runFlags struct.
// It uses ContinueOnError with discarded output so callers can handle
// errors themselves without spurious flag-package output.
//
// After parsing, ParseRunFlags calls buildRamp to pre-validate the ramp
// fields and stores the result in rf.BuiltRamp. Any ramp construction
// error is surfaced here so callers can exit early without opening
// external connections.
func ParseRunFlags(args []string) (runFlags, error) {
	var rf runFlags
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rf.registerOn(fs)
	if err := fs.Parse(args); err != nil {
		return rf, fmt.Errorf("parsing run flags: %w", err)
	}
	// Detect whether --abort-window-max-samples was explicitly passed so the
	// auto-size logic in runRun can distinguish "user set" from "default".
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "abort-window-max-samples" {
			rf.Abort.WindowMaxSamplesSet = true
		}
	})
	// Pre-validate ramp fields and store the built *Ramp so executeRun can
	// read rf.BuiltRamp directly without rebuilding (Phase 2 pre-condition).
	// An error here is returned to the caller (runRun exits 2).
	builtRamp, err := buildRamp(rf.Ramp.From, rf.Ramp.To, rf.Ramp.Duration, rf.Ramp.Shape)
	if err != nil {
		return rf, fmt.Errorf("ramp flags: %w", err)
	}
	rf.BuiltRamp = builtRamp
	return rf, nil
}

// registerOn binds all run-subcommand flags to the given FlagSet.
// Defaults and help strings exactly match the original inline flag.*Var
// block in runRun to preserve the --help output.
func (rf *runFlags) registerOn(fs *flag.FlagSet) {
	fs.StringVar(&rf.Preset, "preset", "", "preset name (required; see 'loadgen presets' for available)")
	fs.Int64Var(&rf.Seed, "seed", 42, "RNG seed")
	fs.DurationVar(&rf.Duration, "duration", 60*time.Second, "run duration")
	fs.IntVar(&rf.Rate, "rate", 500, "target msgs/sec")
	fs.DurationVar(&rf.Warmup, "warmup", 10*time.Second, "warmup window (samples discarded)")
	fs.StringVar(&rf.Inject, "inject", "frontdoor", "injection point: frontdoor|canonical")
	fs.StringVar(&rf.Scenario, "scenario", "messaging-pipeline", "scenario: messaging-pipeline|history-read|search-read|room-rpc|raw-consistency|room-open|read-receipts|large-room-broadcast|notification-fanout|message-mutate|subscription-churn|first-dm|auth-load|federation-lag")
	fs.DurationVar(&rf.RequestTimeout, "request-timeout", 5*time.Second, "per-request timeout for read scenarios")
	fs.BoolVar(&rf.AutoWarmup.Enabled, "auto-warmup", true, "run a brief messaging-pipeline phase to populate message IDs before read scenarios that need them")
	fs.IntVar(&rf.AutoWarmup.Rate, "auto-warmup-rate", 200, "publish rate (rps) during the auto-warmup phase")
	fs.DurationVar(&rf.Progress.Interval, "progress-interval", 10*time.Second, "live progress log interval; 0 disables")
	fs.BoolVar(&rf.Readiness.Skip, "skip-readiness", false, "skip the pre-run readiness probe for read scenarios")
	fs.DurationVar(&rf.Readiness.Timeout, "readiness-timeout", 30*time.Second, "deadline for the readiness probe to succeed")
	fs.IntVar(&rf.Ramp.From, "ramp-from", 0, "starting rate (rps) for a ramped run; 0 disables ramping")
	fs.IntVar(&rf.Ramp.To, "ramp-to", 0, "ending rate (rps) for a ramped run; 0 disables ramping")
	fs.DurationVar(&rf.Ramp.Duration, "ramp-duration", 0, "time to climb from --ramp-from to --ramp-to")
	fs.StringVar(&rf.Ramp.Shape, "ramp-shape", "linear", "ramp curve: linear|exponential")
	fs.IntVar(&rf.Conn.Connections, "connections", 1, "number of NATS data connections (per-user fan-out); 1 reuses the observer connection")
	fs.StringVar(&rf.NATSCredsDir, "nats-creds-dir", "", "directory of *.creds files; data conns rotate through them (C2 prep — auth-service must be in compose stack for SUT-side validation)")
	fs.IntVar(&rf.Abort.P99Ms, "abort-on-p99-ms", 0, "abort the run if the p99 of the abort window's latency stays over this for --abort-p99-sustain; 0 disables")
	fs.DurationVar(&rf.Abort.P99Sustain, "abort-p99-sustain", 30*time.Second, "sustain window for the p99 abort threshold")
	fs.Float64Var(&rf.Abort.ErrorPct, "abort-on-error-pct", 0, "abort the run if error rate stays over this fraction (0..1) for --abort-error-sustain; 0 disables")
	fs.DurationVar(&rf.Abort.ErrorSustain, "abort-error-sustain", 10*time.Second, "sustain window for the error-rate abort threshold")
	fs.DurationVar(&rf.Liveness.Interval, "liveness-interval", 10*time.Second, "mid-run SUT liveness probe interval; 0 disables. Default 10s × 3 failures = 30s detection so the watcher can fire on the default 60s --duration.")
	fs.IntVar(&rf.Liveness.Failures, "liveness-failures", 3, "consecutive liveness probe failures required to abort the run")
	fs.DurationVar(&rf.Liveness.Timeout, "liveness-timeout", 5*time.Second, "per-probe timeout. Aligned with --request-timeout default so a slow-but-up SUT trips the saturation watcher (exit 2) before the liveness watcher (exit 3).")
	fs.IntVar(&rf.JS.AsyncMaxPending, "js-async-max-pending", 4096, "S5: max in-flight async JetStream publishes for canonical inject; 0 falls back to sync js.PublishMsg (legacy / bisection)")
	fs.IntVar(&rf.Abort.WindowMaxSamples, "abort-window-max-samples", 10000, "S3: cap on the abort/progress latency ring buffer; 0 disables the cap (legacy). Bounds the per-tick percentile sort under sustained high publish rates. WARNING: when peak_rps × max(abort-*-sustain) > cap, retention is compressed below the sustain interval and the abort watcher cannot fire; it emits a slog.Warn 'abort watcher deafened by sample cap' so the silent no-fire is detectable. Size cap >= peak_rps × max_sustain to keep the watcher functional.")
	fs.StringVar(&rf.CSV, "csv", "", "optional csv output path")
	fs.DurationVar(&rf.RAW.PollInterval, "raw-poll-interval", 10*time.Millisecond,
		"raw-consistency scenario: interval between visibility polls per path")
	fs.DurationVar(&rf.RAW.Timeout, "raw-timeout", 5*time.Second,
		"raw-consistency scenario: per-message poll timeout before declaring not-visible")
	fs.DurationVar(&rf.SearchSync.PollInterval, "search-sync-poll-interval", 250*time.Millisecond,
		"search-sync-lag scenario: interval between search.messages visibility polls (ES refresh_interval default is 30s, so sub-100ms polls burn RPCs without resolution gain)")
	fs.DurationVar(&rf.SearchSync.Timeout, "search-sync-timeout", 90*time.Second,
		"search-sync-lag scenario: per-message poll timeout before declaring not-visible (3x default ES refresh_interval to cover the long tail)")
	fs.DurationVar(&rf.SearchSync.ACLWait, "search-sync-acl-wait", 35*time.Second,
		"search-sync-lag scenario: wait after the one-shot user-room ACL bootstrap before the main publish/poll loop starts (covers ES refresh_interval + bulk-flush slack)")
	fs.BoolVar(&rf.SearchSync.SkipACLBootstrap, "search-sync-skip-acl-bootstrap", false,
		"search-sync-lag scenario: skip the one-shot user-room ACL bootstrap + 35s wait at Run start. Use only when the ACL doc is already populated from a prior run; leave off for the first run after `loadgen seed`")
	fs.Float64Var(&rf.ReceiptCoverage, "receipt-coverage", 0.6,
		"read-receipts scenario: fraction of recipients to fire MessageRead for per published message")
	fs.DurationVar(&rf.Settle.Timeout, "settle-timeout", 30*time.Second, "settle phase: max time to wait for probes to succeed before declaring failure")
	fs.DurationVar(&rf.Settle.Interval, "settle-interval", 500*time.Millisecond, "settle phase: poll interval between probe rounds")
	fs.IntVar(&rf.Settle.Probes, "settle-probes", 20, "settle phase: number of recent message IDs to probe (0 disables)")
	fs.BoolVar(&rf.AllowConcurrent, "allow-concurrent", false, "allow multiple concurrent loadgen runs against the same SUT (default refuses to start when another active run is detected)")
	fs.IntVar(&rf.MaxInFlight, "max-in-flight", 0, "override MAX_IN_FLIGHT env (>0 takes effect; 0 = use env default 200). Caps concurrent publishes/pollers; lower it when search-sync-lag dashboards show dropped_inflight dominance.")
	fs.DurationVar(&rf.RunTTL, "run-ttl", 2*time.Hour, "max age for an active runlock entry before it is considered orphaned and ignored")
	fs.IntVar(&rf.MutateRate, "mutate-rate", 0,
		"message-mutate scenario: mutations per second; 0 uses scenario default (5)")
	fs.StringVar(&rf.EditAgeDistribution, "edit-age-distribution", "0.7,0.3",
		"message-mutate scenario: typo,correction fractions (last 30s vs 24h)")
	fs.IntVar(&rf.ChurnRate, "churn-rate", 0,
		"subscription-churn scenario: churn events per second; 0 uses scenario default (5)")
	fs.BoolVar(&rf.FirstDMRecycle, "first-dm-recycle", false,
		"first-dm scenario: wrap around the user-pair pool when exhausted (default: exit cleanly)")
	fs.DurationVar(&rf.AuthStormPeriod, "auth-storm-period", 0,
		"auth-load: interval between reconnect-storm events (0 = single one-shot drop at T+30s)")
	fs.StringVar(&rf.AuthURL, "auth-url", "",
		"auth-load: auth-service base URL (overrides AUTH_SERVICE_URL env; default http://auth-service:8080)")
	fs.BoolVar(&rf.FederationFlap, "federation-flap", false,
		"federation-lag: enable periodic site-b stop/restart to measure INBOX backlog drain")
	fs.BoolVar(&rf.FederationCrossRead, "federation-cross-read", false,
		"federation-lag: enable cross-site read sub-mode (siteA user reads siteB room history)")
	fs.DurationVar(&rf.FlapPeriod, "flap-period", 60*time.Second,
		"federation-lag: interval between flap events")
	fs.DurationVar(&rf.FlapDown, "flap-down", 30*time.Second,
		"federation-lag: site-b downtime per flap event")
	fs.StringVar(&rf.FederationSecondaryNATSURL, "federation-secondary-nats-url", "",
		"federation-lag: NATS URL for site-b (typically nats://site-b-nats:4222 when running the federation Compose overlay)")
}
