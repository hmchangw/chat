package main

// dispatch_members.go holds the subcommand handlers for the room-member load
// test workload (PR #203): seed/teardown branches for `--workload=members`,
// and the `members-sustained` / `members-capacity` subcommand entry points.
// Kept separate from dispatch.go so the file boundary cleanly reflects the
// workload split.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// ---------------------------------------------------------------- seed/teardown

func runSeedMembers(ctx context.Context, cfg *config, preset string, seed int64) int {
	p, ok := BuiltinMembersPreset(preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown members preset: %s\n", preset)
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
	fixtures, pools := BuildMembersFixtures(&p, seed, cfg.SiteID)
	if err := Seed(ctx, db, &fixtures); err != nil {
		slog.Error("seed", "error", err)
		return 1
	}
	if err := SeedRoomKeys(ctx, keyStore, fixtures.RoomKeys); err != nil {
		slog.Error("seed room keys", "error", err)
		return 1
	}
	candCount := 0
	for _, ids := range pools {
		candCount += len(ids)
	}
	slog.Info("seed complete (members)",
		"preset", p.Name,
		"users", len(fixtures.Users),
		"rooms", len(fixtures.Rooms),
		"subs", len(fixtures.Subscriptions),
		"roomKeys", len(fixtures.RoomKeys),
		"candidatePoolTotal", candCount)
	return 0
}

func runTeardownMembers(ctx context.Context, cfg *config, preset string, seed int64) int {
	p, ok := BuiltinMembersPreset(preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown members preset: %s\n", preset)
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
	fixtures, _ := BuildMembersFixtures(&p, seed, cfg.SiteID)
	roomIDs := roomIDsOf(fixtures.Rooms)
	if err := Teardown(ctx, db); err != nil {
		slog.Error("teardown", "error", err)
		return 1
	}
	if err := TeardownRoomKeys(ctx, keyStore, roomIDs); err != nil {
		slog.Error("teardown room keys", "error", err)
		return 1
	}
	slog.Info("teardown complete (members)")
	return 0
}

// roomIDsOf is a small extractor; the seed/teardown paths above use it once
// and the capacity summary path uses it again. Cheap allocation, sharp edge.
func roomIDsOf(rooms []model.Room) []string {
	out := make([]string, len(rooms))
	for i := range rooms {
		out[i] = rooms[i].ID
	}
	return out
}

// ---------------------------------------------------------------- subcommands

func runMembersSustained(ctx context.Context, cfg *config, args []string) int {
	fs := flag.NewFlagSet("members-sustained", flag.ExitOnError)
	preset := fs.String("preset", "", "members preset name")
	seed := fs.Int64("seed", 42, "RNG seed")
	duration := fs.Duration("duration", 60*time.Second, "run duration")
	rate := fs.Int("rate", 100, "target req/sec")
	warmup := fs.Duration("warmup", 10*time.Second, "warmup window (samples discarded)")
	inject := fs.String("inject", "frontdoor", "frontdoor|canonical")
	shapeFlag := fs.String("shape", "users", "users|orgs|channels|mixed (v1: users only)")
	usersPerAdd := fs.Int("users-per-add", 10, "users per add request")
	csvPath := fs.String("csv", "", "optional CSV output path")
	_ = fs.Parse(args)

	if *preset == "" {
		fmt.Fprintln(os.Stderr, "--preset required")
		return 2
	}
	p, ok := BuiltinMembersPreset(*preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown members preset: %s\n", *preset)
		return 2
	}
	injectMode, err := ParseInjectMode(*inject)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	shape, err := ParseShape(*shapeFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	if err := ValidateInjectShape(injectMode, shape); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	if *usersPerAdd <= 0 {
		fmt.Fprintln(os.Stderr, "--users-per-add must be > 0")
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
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("metrics server stopped", "error", err)
		}
	}()

	fixtures, pools := BuildMembersFixtures(&p, *seed, cfg.SiteID)
	owners := OwnersByRoom(&fixtures)
	collector := NewMemberCollector(metrics, p.Name, injectMode)

	e2Sub, err := nc.NatsConn().Subscribe(subject.RoomMemberEventWildcard(), func(m *nats.Msg) {
		roomID, accounts, ok := ParseMemberAddBroadcast(m.Data)
		if !ok {
			return
		}
		collector.RecordBroadcast(roomID, accounts, time.Now())
	})
	if err != nil {
		slog.Error("subscribe e2", "error", err)
		return 1
	}
	defer func() { _ = e2Sub.Unsubscribe() }()

	var publisher MemberPublisher
	var frontdoor *frontdoorMemberPublisher
	switch injectMode {
	case InjectFrontdoor:
		frontdoor, err = newFrontdoorMemberPublisher(nc.NatsConn(), cfg.SiteID, func(corrID string, body []byte, at time.Time) {
			collector.RecordReply(corrID, string(body), at)
		})
		if err != nil {
			slog.Error("frontdoor publisher", "error", err)
			return 1
		}
		defer frontdoor.Close()
		publisher = frontdoor
	case InjectCanonical:
		publisher = newCanonicalMemberPublisher(js, cfg.SiteID)
	}

	samplerCtx, cancelSamplers := context.WithCancel(ctx)
	defer cancelSamplers()
	sampler := NewConsumerSampler(js, stream.Rooms(cfg.SiteID).Name, "room-worker", metrics, time.Second)
	var samplerWG sync.WaitGroup
	samplerWG.Add(1)
	go func() {
		defer samplerWG.Done()
		sampler.Run(samplerCtx)
	}()

	warmupDeadline := time.Now().Add(*warmup)
	genCfg := SustainedMembersConfig{
		Preset:         &p,
		Fixtures:       &fixtures,
		Pools:          pools,
		Owners:         owners,
		Rate:           *rate,
		UsersPerAdd:    *usersPerAdd,
		Inject:         injectMode,
		Shape:          shape,
		Publisher:      publisher,
		Metrics:        metrics,
		Collector:      collector,
		WarmupDeadline: warmupDeadline,
		MaxInFlight:    cfg.MaxInFlight,
	}
	gen := NewSustainedMembersGenerator(&genCfg, *seed)

	runCtx, cancelRun := context.WithTimeout(ctx, *duration)
	defer cancelRun()
	genErr := gen.Run(runCtx)
	// Drain trailing replies / broadcasts. The 2s window matches the
	// messaging-pipeline drain budget; coordinated-omission tracker handles
	// the in-flight accounting.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 2*time.Second)
	<-drainCtx.Done()
	cancelDrain()
	collector.DiscardBefore(warmupDeadline)
	missingReplies, missingBroadcasts := collector.Finalize()

	cancelSamplers()
	samplerWG.Wait()

	shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
	_ = metricsSrv.Shutdown(shutCtx)
	cancelShut()
	_ = nc.Drain()

	switch {
	case errors.Is(genErr, ErrPoolsExhausted):
		slog.Warn("aborted early", "reason", "pools exhausted")
	case genErr != nil:
		slog.Error("generator error", "error", genErr)
	}

	mfs, _ := metrics.Registry.Gather()
	pubErrs := int(gatheredCounterValue(mfs, "loadgen_member_publish_errors_total", "reason", "publish"))
	rsErrs := collector.RoomServiceErrorCount()
	sentWarmup := int(gatheredCounterValue(mfs, "loadgen_member_published_total", "phase", "warmup"))
	sentMeasured := int(gatheredCounterValue(mfs, "loadgen_member_published_total", "phase", "measured"))
	sent := sentWarmup + sentMeasured
	measured := *duration - *warmup
	var actualRate float64
	if measured > 0 {
		actualRate = float64(sentMeasured) / measured.Seconds()
	}

	summary := MembersSummary{
		Preset: p.Name, Site: cfg.SiteID, Inject: string(injectMode), Shape: string(shape),
		Seed: *seed, TargetRate: *rate, ActualRate: actualRate,
		Duration: *duration, Warmup: *warmup, UsersPerAdd: *usersPerAdd,
		Sent: sent, SentMeasured: sentMeasured,
		PublishErrors: pubErrs, RoomServiceErrors: rsErrs,
		MissingReplies: missingReplies, MissingBroadcasts: missingBroadcasts,
		E1:      ComputePercentiles(collector.E1Samples()),
		E2:      ComputePercentiles(collector.E2Samples()),
		E1Count: collector.E1Count(), E2Count: collector.E2Count(),
		Consumers: []ConsumerStat{sampler.Snapshot()},
	}
	if err := PrintMembersSummary(os.Stdout, &summary); err != nil {
		slog.Warn("print summary", "error", err)
	}
	if *csvPath != "" {
		if err := writeMembersCSV(*csvPath, collector); err != nil {
			slog.Error("csv export", "error", err)
		}
	}
	totalErrs := summary.PublishErrors + summary.RoomServiceErrors + summary.MissingReplies + summary.MissingBroadcasts
	return DetermineExitCode(summary.SentMeasured, totalErrs)
}

func runMembersCapacity(ctx context.Context, cfg *config, args []string) int {
	fs := flag.NewFlagSet("members-capacity", flag.ExitOnError)
	preset := fs.String("preset", "", "members preset name")
	seed := fs.Int64("seed", 42, "RNG seed")
	inject := fs.String("inject", "frontdoor", "frontdoor|canonical")
	shapeFlag := fs.String("shape", "users", "users|orgs|channels|mixed (v1: users only)")
	usersPerAdd := fs.Int("users-per-add", 10, "users per add request")
	targetSize := fs.Int("target-size", 0, "stop each room when its member count >= target-size (required)")
	maxRate := fs.Int("max-rate", 0, "optional cap on per-room req/sec; 0 = sequential pacing only")
	e2Timeout := fs.Duration("e2-timeout", 30*time.Second, "max wait for broadcast per add")
	csvPath := fs.String("csv", "", "optional CSV output path")
	_ = fs.Parse(args)

	if *preset == "" {
		fmt.Fprintln(os.Stderr, "--preset required")
		return 2
	}
	if *targetSize <= 0 {
		fmt.Fprintln(os.Stderr, "--target-size required and must be > 0")
		return 2
	}
	p, ok := BuiltinMembersPreset(*preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown members preset: %s\n", *preset)
		return 2
	}
	injectMode, err := ParseInjectMode(*inject)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	shape, err := ParseShape(*shapeFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	if err := ValidateInjectShape(injectMode, shape); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	if *usersPerAdd <= 0 {
		fmt.Fprintln(os.Stderr, "--users-per-add must be > 0")
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
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("metrics server stopped", "error", err)
		}
	}()

	fixtures, pools := BuildMembersFixtures(&p, *seed, cfg.SiteID)
	owners := OwnersByRoom(&fixtures)
	collector := NewMemberCollector(metrics, p.Name, injectMode)

	e2Sub, err := nc.NatsConn().Subscribe(subject.RoomMemberEventWildcard(), func(m *nats.Msg) {
		roomID, accounts, ok := ParseMemberAddBroadcast(m.Data)
		if !ok {
			return
		}
		collector.RecordBroadcast(roomID, accounts, time.Now())
	})
	if err != nil {
		slog.Error("subscribe e2", "error", err)
		return 1
	}
	defer func() { _ = e2Sub.Unsubscribe() }()

	var publisher MemberPublisher
	var frontdoor *frontdoorMemberPublisher
	switch injectMode {
	case InjectFrontdoor:
		frontdoor, err = newFrontdoorMemberPublisher(nc.NatsConn(), cfg.SiteID, func(corrID string, body []byte, at time.Time) {
			collector.RecordReply(corrID, string(body), at)
		})
		if err != nil {
			slog.Error("frontdoor publisher", "error", err)
			return 1
		}
		defer frontdoor.Close()
		publisher = frontdoor
	case InjectCanonical:
		publisher = newCanonicalMemberPublisher(js, cfg.SiteID)
	}

	genCfg := CapacityMembersConfig{
		Preset:      &p,
		Fixtures:    &fixtures,
		Pools:       pools,
		Owners:      owners,
		UsersPerAdd: *usersPerAdd,
		Inject:      injectMode,
		Shape:       shape,
		TargetSize:  *targetSize,
		MaxRate:     *maxRate,
		Publisher:   publisher,
		Metrics:     metrics,
		Collector:   collector,
		E2Timeout:   *e2Timeout,
	}
	gen := NewCapacityMembersGenerator(&genCfg)
	if err := gen.Run(ctx); err != nil {
		slog.Error("generator error", "error", err)
	}
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 2*time.Second)
	<-drainCtx.Done()
	cancelDrain()
	collector.Finalize()

	shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
	_ = metricsSrv.Shutdown(shutCtx)
	cancelShut()
	_ = nc.Drain()

	finals := map[string]int{}
	mfs, _ := metrics.Registry.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "loadgen_member_room_size" {
			continue
		}
		for _, mt := range mf.GetMetric() {
			var rid string
			for _, l := range mt.GetLabel() {
				if l.GetName() == "room_id" {
					rid = l.GetValue()
				}
			}
			finals[rid] = int(mt.GetGauge().GetValue())
		}
	}
	pubErrs := int(gatheredCounterValue(mfs, "loadgen_member_publish_errors_total", "reason", "publish"))
	timeouts := int(gatheredCounterValue(mfs, "loadgen_member_publish_errors_total", "reason", "timeout"))

	edges := []int{0, *targetSize / 4, *targetSize / 2, (*targetSize * 3) / 4, *targetSize + 1}
	buckets := computeSizeBuckets(collector, finals, edges)

	summary := CapacitySummary{
		Preset: p.Name, Site: cfg.SiteID, Inject: string(injectMode), Shape: string(shape),
		Seed: *seed, UsersPerAdd: *usersPerAdd, TargetSize: *targetSize,
		PublishErrors: pubErrs, Timeouts: timeouts,
		Buckets: buckets, FinalSizes: finals,
	}
	if err := PrintCapacitySummary(os.Stdout, &summary); err != nil {
		slog.Warn("print summary", "error", err)
	}
	if *csvPath != "" {
		if err := writeMembersCSV(*csvPath, collector); err != nil {
			slog.Error("csv export", "error", err)
		}
	}
	return 0
}

// ---------------------------------------------------------------- CSV / buckets

func writeMembersCSV(path string, c *MemberCollector) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create csv: %w", err)
	}
	defer func() { _ = f.Close() }()
	var rows []CSVSample
	for i, d := range c.E1Samples() {
		rows = append(rows, CSVSample{RowIndex: int64(i), Metric: "E1", LatencyNs: d.Nanoseconds()})
	}
	for i, d := range c.E2Samples() {
		rows = append(rows, CSVSample{RowIndex: int64(i), Metric: "E2", LatencyNs: d.Nanoseconds()})
	}
	return WriteCSV(f, rows)
}

// computeSizeBuckets is intentionally simple in v1 — it returns one row per
// bucket with the aggregate E1/E2 percentiles for samples whose source room's
// FINAL size fell in that bucket. (Per-sample size tracking is a v2
// enhancement; for now we treat each room's full latency tape as belonging
// to its final-size bucket.)
func computeSizeBuckets(c *MemberCollector, finals map[string]int, edges []int) []SizeBucket {
	out := make([]SizeBucket, 0, len(edges)-1)
	for i := 0; i < len(edges)-1; i++ {
		out = append(out, SizeBucket{Lower: edges[i], Upper: edges[i+1]})
	}
	for _, sz := range finals {
		idx := BucketIndex(sz, edges)
		if idx < 0 {
			continue
		}
		out[idx].Count++
	}
	e1 := ComputePercentiles(c.E1Samples())
	e2 := ComputePercentiles(c.E2Samples())
	for i := range out {
		if out[i].Count > 0 {
			out[i].E1 = e1
			out[i].E2 = e2
		}
	}
	return out
}
