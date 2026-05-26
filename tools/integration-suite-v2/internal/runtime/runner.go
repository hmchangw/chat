package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"gopkg.in/yaml.v3"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeystore"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/catalog"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/fixtures"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/verbs"
	"github.com/hmchangw/chat/tools/integration-suite-v2/seed"
)

// Config holds the runner's wiring.
type Config struct {
	AuthURL            string
	NATSURL            string
	NATSCredsFile      string // optional; when set the runner opens an admin connection and wires JetStream readers
	MongoURI           string
	MongoDB            string
	ValkeyAddr         string // optional; when set the runner seeds pure-room-worker room keys at startup
	SiteID             string
	ScenariosDir       string
	CatalogsDir        string
	OutputPath         string
	ApprovedOutputPath string // optional; when set, writes a filtered approved-only report
	PerformancePath    string // optional; persistent per-test latest/best/worst pass history across runs
	RepoRoot           string // optional; passed to git for HEAD + latest-merge commit lookup
}

// Run executes the entire suite: load catalogs, seed cast, walk every
// scenario, run its happy case, write the report.
func Run(ctx context.Context, cfg *Config) (*RunReport, error) {
	runID := newRunID()
	SetRunID(runID)
	report := &RunReport{
		RunID:    runID,
		StartISO: time.Now().UTC().Format(time.RFC3339),
	}
	startTime := time.Now()

	cat, err := catalog.Load(cfg.CatalogsDir)
	if err != nil {
		return nil, fmt.Errorf("load catalogs: %w", err)
	}
	profileLookup := buildProfileLookup(cat)

	var castSpec fixtures.CastSpec
	if err := loadCastSpec(filepath.Join(cfg.CatalogsDir, "fixture-cast.yaml"), &castSpec); err != nil {
		return nil, fmt.Errorf("load cast spec: %w", err)
	}
	seeder := fixtures.NewSeeder(cfg.AuthURL)
	cast, err := seeder.Seed(ctx, castSpec)
	if err != nil {
		return nil, fmt.Errorf("seed cast: %w", err)
	}

	// Join seed JSON user records onto CastUser so the substitution
	// context can expose every production-shape field of the picked
	// fixture via ${<placeholder>.<field>}. Convention: cast.account ==
	// seed.account.
	seedUsers, _, _, err := seed.Decoded()
	if err != nil {
		return nil, fmt.Errorf("decode seed users: %w", err)
	}
	seedByAccount := map[string]*model.User{}
	for i := range seedUsers {
		seedByAccount[seedUsers[i].Account] = &seedUsers[i]
	}
	for i := range cast.Users {
		if u, ok := seedByAccount[cast.Users[i].Account]; ok {
			cast.Users[i].UserDoc = u
		}
	}

	mongoClient, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()

	// Optional Valkey key seeding for pure room-worker scenarios. When
	// VALKEY_ADDR is set the runner connects once at startup and writes
	// a fresh keypair for every roomID listed in seed/room-keys.json.
	// Keys are stable across scenarios (Mongo wipe doesn't affect them);
	// pure-room-worker scenarios publish canonical events with one of
	// these literal roomIDs so room-worker's Valkey gate passes.
	if cfg.ValkeyAddr != "" {
		keyStore, err := roomkeystore.NewValkeyStore(roomkeystore.Config{
			Addr:        cfg.ValkeyAddr,
			GracePeriod: 24 * time.Hour,
		})
		if err != nil {
			return nil, fmt.Errorf("valkey connect: %w", err)
		}
		if err := seed.LoadRoomKeys(ctx, keyStore); err != nil {
			return nil, fmt.Errorf("seed room keys: %w", err)
		}
	} else {
		slog.Warn("VALKEY_ADDR not set; pure-room-worker scenarios that need a pre-seeded room key will fail at room-worker's Valkey gate")
	}

	verbReg := verbs.NewRegistry()
	verbReg.Register("NATSRequestExecutor", verbs.NewNATSRequest(cfg.NATSURL))
	verbReg.Register("JetStreamPublishExecutor", verbs.NewJetStreamPublish(cfg.NATSURL))

	matcherReg := matchers.NewRegistry()

	readerReg := readers.NewRegistry()
	readerReg.Register("mongo.rooms", readers.NewMongoRoomsReader(mongoClient, cfg.MongoDB))
	replyReader := readers.NewNATSReplyReader()
	readerReg.Register("reply", replyReader)
	readerReg.Register("logs.room-service",
		readers.NewContainerLogsReader("room-service", "room-service", "logs.room-service"))
	readerReg.Register("logs.room-worker",
		readers.NewContainerLogsReader("room-worker", "room-worker", "logs.room-worker"))

	// Optional admin NATS connection — required for JetStream observation
	// readers. Disabled when NATSCredsFile is not set (developer can still
	// run scenarios that only use `reply` / `mongo.rooms` / `logs.*`).
	if cfg.NATSCredsFile != "" {
		adminConn, err := nats.Connect(cfg.NATSURL, nats.UserCredentials(cfg.NATSCredsFile), nats.Name("integration-suite-v2/runner"))
		if err != nil {
			return nil, fmt.Errorf("nats admin connect: %w", err)
		}
		defer adminConn.Drain() //nolint:errcheck
		readerReg.Register("jetstream.rooms-canonical",
			readers.NewJetStreamSubjectReader(
				adminConn,
				fmt.Sprintf("ROOMS_%s", cfg.SiteID),
				fmt.Sprintf("chat.room.canonical.%s.>", cfg.SiteID),
				"jetstream.rooms-canonical",
				"room-service",
			))
	} else {
		slog.Warn("NATS_CREDS_FILE not set; JetStream observation readers disabled (scenarios that reference jetstream.* will fail to resolve their readers)")
	}

	dispatcher := &Dispatcher{VerbReg: verbReg, ReplyRdr: replyReader}
	observer := &Observer{Readers: readerReg}

	scenarioFiles, err := findScenarios(cfg.ScenariosDir)
	if err != nil {
		return nil, fmt.Errorf("find scenarios: %w", err)
	}
	mongoDB := mongoClient.Database(cfg.MongoDB)
	for _, sf := range scenarioFiles {
		s, err := scenario.LoadFile(sf)
		if err != nil {
			return nil, fmt.Errorf("load scenario %s: %w", sf, err)
		}
		// Per-scenario reload: drop + InsertMany users/rooms/subscriptions
		// from seed/*.json so each scenario starts from a byte-identical
		// world. Locked in corrections-spec §3.0.
		if err := seed.LoadAll(ctx, mongoDB); err != nil {
			return nil, fmt.Errorf("seed mongo for %s: %w", s.Name, err)
		}
		caseStart := time.Now()
		caseRep, err := runOneCase(ctx, s, cast, cfg, dispatcher, observer, matcherReg, profileLookup)
		if err != nil {
			return nil, fmt.Errorf("run %s: %w", s.Name, err)
		}
		caseRep.Duration = time.Since(caseStart)
		report.Cases = append(report.Cases, caseRep)
	}

	report.Duration = time.Since(startTime).Round(time.Millisecond).String()

	// Git info for the report heading.
	if cfg.RepoRoot != "" {
		report.Git = CollectGitInfo(cfg.RepoRoot)
	}

	// Performance history: load, merge this run's passes, save.
	if cfg.PerformancePath != "" {
		perf, err := LoadPerformance(cfg.PerformancePath)
		if err != nil {
			return nil, fmt.Errorf("load performance: %w", err)
		}
		commit := report.Git.HEAD
		if commit == "" {
			commit = report.RunID
		}
		perf.Update(report.Cases, commit, report.StartISO)
		if err := SavePerformance(cfg.PerformancePath, perf); err != nil {
			return nil, fmt.Errorf("save performance: %w", err)
		}
		report.Perf = perf
	}

	if cfg.OutputPath != "" {
		if err := Write(cfg.OutputPath, report); err != nil {
			return nil, fmt.Errorf("write report: %w", err)
		}
	}
	if cfg.ApprovedOutputPath != "" {
		if err := WriteApproved(cfg.ApprovedOutputPath, report); err != nil {
			return nil, fmt.Errorf("write approved report: %w", err)
		}
	}
	return report, nil
}

func runOneCase(
	ctx context.Context,
	s *scenario.Scenario,
	cast *fixtures.Cast,
	cfg *Config,
	dispatcher *Dispatcher,
	observer *Observer,
	matcherReg *matchers.Registry,
	profileLookup AbilityProfileLookup,
) (CaseReport, error) {
	res, err := scenario.Resolve(s, cast)
	if err != nil {
		return CaseReport{ScenarioName: s.Name, Subset: "happy", Status: status(s), Kind: kindOf(s), Verdict: Verdict{Outcome: "fail", Reason: err.Error()}}, nil
	}

	// Build substitution context. Placeholders come from the joined
	// seed user records exposed via CastUser.UserDoc; Services come
	// from the runner config (NATSCredsFile if set); Site from cfg.
	// Input is populated by Fire after substitution.
	subCtx := &Context{
		Site:     cfg.SiteID,
		Services: buildServiceCreds(cfg),
	}
	subCtx.Placeholders, err = buildPlaceholderMaps(res)
	if err != nil {
		return CaseReport{ScenarioName: s.Name, Subset: "happy", Status: status(s), Kind: kindOf(s), Verdict: Verdict{Outcome: "fail", Reason: err.Error()}}, nil
	}

	// Generate this scenario's traceparent up-front so the observer can
	// hand it to readers and apply scope-aware synthetic stamping for
	// events from in-scope services that lack native trace propagation.
	traceparent := NewTraceparent()
	traceID := TraceIDFromTraceparent(traceparent)

	inScopeServices := make([]string, 0, len(s.Sequence))
	for i := range s.Sequence {
		inScopeServices = append(inScopeServices, s.Sequence[i].Service)
	}

	obsCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	startTime := time.Now()
	events := observer.Start(obsCtx, traceparent, inScopeServices, startTime)

	fr, err := dispatcher.Fire(ctx, s, res, subCtx, traceparent)
	if err != nil {
		return CaseReport{ScenarioName: s.Name, Subset: "happy", Status: status(s), Kind: kindOf(s), Verdict: Verdict{Outcome: "fail", Reason: err.Error()}}, nil
	}

	gathered, _ := GatherUntilQuiet(events, traceID, inScopeServices, 200*time.Millisecond, 5*time.Second)

	v := Classify(s, gathered, traceID, matcherReg, profileLookup, subCtx, startTime)
	if fr.Outcome.Err != nil && v.Outcome == "fail" {
		v.Reason = fr.Outcome.Err.Error()
	}
	return CaseReport{ScenarioName: s.Name, Subset: "happy", Status: status(s), Kind: kindOf(s), Verdict: v}, nil
}

// buildServiceCreds returns the service-credential map exposed via
// ${service.<name>} in scenarios. Today: the chatapp backend
// service-level NATS creds file (the docker-local/backend.creds the
// suite is invoked with via NATS_CREDS_FILE). When that env var is
// unset, the map is empty and scenarios that reference ${service.*}
// will fail at credential resolution — which is the right behavior
// (loud failure beats silent fallthrough to user-level creds).
func buildServiceCreds(cfg *Config) map[string]Credential {
	out := map[string]Credential{}
	if cfg.NATSCredsFile != "" {
		out["backend"] = Credential{
			Account:   "backend",
			CredsFile: cfg.NATSCredsFile,
		}
	}
	return out
}

// buildProfileLookup returns an AbilityProfileLookup backed by the
// loaded catalog's service profiles. For each service, it flattens
// every trigger's `on_trigger.writes` entries into a per-service set
// of locations. The returned lookup answers "does <service>'s ability
// profile allow it to write to <location>?" — which the verdict
// classifier uses to filter background activity (events that arrive
// without our scenario's trace but are produced by a service whose
// profile says it legitimately writes to that location).
func buildProfileLookup(cat *catalog.Catalog) AbilityProfileLookup {
	writes := map[string]map[string]bool{}
	for _, svc := range cat.Services {
		if writes[svc.Name] == nil {
			writes[svc.Name] = map[string]bool{}
		}
		for _, trig := range svc.Triggers {
			otAny, ok := trig["on_trigger"]
			if !ok {
				continue
			}
			items, _ := otAny.([]any)
			for _, item := range items {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				w, _ := m["writes"].(string)
				if w != "" {
					writes[svc.Name][w] = true
				}
			}
		}
	}
	return func(serviceName string, ev readers.Event) bool {
		if set, ok := writes[serviceName]; ok {
			return set[ev.Location]
		}
		return false
	}
}

// buildPlaceholderMaps converts a scenario.Resolution into the
// map[name]map[field]any shape the substitution Context expects.
// When CastUser.UserDoc is populated (via seed-join in Run), every
// field of the production-shape user record is exposed. Falls back to
// a minimal map for cast users without a seed join.
func buildPlaceholderMaps(res *scenario.Resolution) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for name, u := range res.Users {
		if u.UserDoc != nil {
			m, err := ToPlaceholderMap(u.UserDoc)
			if err != nil {
				return nil, fmt.Errorf("placeholder %q: %w", name, err)
			}
			out[name] = m
			continue
		}
		out[name] = map[string]any{
			"id":      u.ID,
			"account": u.Account,
		}
	}
	return out, nil
}

// kindOf returns the scenario's confusion-matrix axis: "positive"
// (asserts the system does the thing) or "negative" (asserts the
// system correctly rejects). Default is "positive" when the
// scenario YAML omits the kind field.
func kindOf(s *scenario.Scenario) string {
	if s.Kind == "negative" {
		return "negative"
	}
	return "positive"
}

func status(s *scenario.Scenario) string {
	if s.Status == "approved" {
		return "approved"
	}
	return "draft"
}

func newRunID() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func loadCastSpec(path string, into *fixtures.CastSpec) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, into)
}

func findScenarios(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".yaml" {
			out = append(out, path)
		}
		return nil
	})
	if err != nil && !strings.Contains(err.Error(), "no such file") {
		return nil, err
	}
	return out, nil
}
