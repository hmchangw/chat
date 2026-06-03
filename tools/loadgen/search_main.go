package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// ParseSearchArm validates and normalizes a benchmark arm flag. Arms are
// case-insensitive; the canonical form is upper-case C/A/B. The arm is carried
// on each request's Variant field, which the search-service honors only when
// SEARCH_BENCH_MODE_ENABLED=true.
func ParseSearchArm(s string) (string, error) {
	switch strings.ToUpper(s) {
	case "C", "A", "B":
		return strings.ToUpper(s), nil
	default:
		return "", fmt.Errorf("unknown arm %q (want C|A|B)", s)
	}
}

func runSearchSustained(ctx context.Context, cfg *config, args []string) int {
	fs := flag.NewFlagSet("search-sustained", flag.ExitOnError)
	preset := fs.String("preset", "", "search preset name (search-small|search-medium|search-large)")
	armFlag := fs.String("arm", "", "benchmark arm: C|A|B (required)")
	seed := fs.Int64("seed", 42, "RNG seed")
	duration := fs.Duration("duration", 0, "run duration (0 = preset default)")
	rate := fs.Int("rate", 0, "target req/sec (0 = preset default)")
	warmup := fs.Duration("warmup", 10*time.Second, "warmup window (samples discarded)")
	requestTimeout := fs.Duration("request-timeout", 5*time.Second, "per-request timeout")
	csvPath := fs.String("csv", "", "optional CSV output path")
	_ = fs.Parse(args)

	if *preset == "" {
		fmt.Fprintln(os.Stderr, "--preset required")
		return 2
	}
	p, ok := BuiltinSearchPreset(*preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown search preset: %s\n", *preset)
		return 2
	}
	arm, err := ParseSearchArm(*armFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}

	// Preset defaults unless overridden on the command line.
	effRate := p.Rate
	if *rate > 0 {
		effRate = *rate
	}
	effDuration := p.Duration
	if *duration > 0 {
		effDuration = *duration
	}

	nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
	if err != nil {
		slog.Error("nats connect", "error", err)
		return 1
	}

	metrics := NewMetrics()
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metrics.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("metrics server stopped", "error", err)
		}
	}()

	now := time.Now().UTC()
	hp, ok := BuiltinHistoryPreset(searchFixturePreset(p.Name))
	if !ok {
		fmt.Fprintf(os.Stderr, "no fixture preset for %s\n", p.Name)
		return 2
	}
	res := BuildHistoryFixtures(&hp, *seed, cfg.SiteID, now)
	collector := NewSearchCollector(metrics, p.Name)
	requester := newNATSSearchRequester(nc.NatsConn())

	warmupDeadline := time.Now().Add(*warmup)
	genCfg := SearchGeneratorConfig{
		Preset:         &p,
		Fixtures:       &res,
		SiteID:         cfg.SiteID,
		Arm:            arm,
		Rate:           effRate,
		RequestTimeout: *requestTimeout,
		Requester:      requester,
		Collector:      collector,
		MaxInFlight:    p.MaxInFlight,
	}
	gen := NewSearchGenerator(&genCfg, *seed)

	runCtx, cancelRun := context.WithTimeout(ctx, effDuration)
	defer cancelRun()
	genErr := gen.Run(runCtx)
	// Drain trailing in-flight replies.
	time.Sleep(2 * time.Second)
	collector.DiscardBefore(warmupDeadline)

	shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
	_ = metricsSrv.Shutdown(shutCtx)
	cancelShut()
	_ = nc.Drain()

	if genErr != nil {
		slog.Warn("generator returned error", "error", genErr)
	}

	measured := effDuration - *warmup
	if err := printSearchSummary(os.Stdout, cfg.SiteID, &p, arm, *seed, effRate, measured, collector); err != nil {
		slog.Warn("print summary", "error", err)
	}
	if *csvPath != "" {
		if err := writeSearchCSVFile(*csvPath, collector); err != nil {
			slog.Error("csv export", "error", err)
		}
	}

	return DetermineExitCode(collector.TotalCount(), collector.TotalErrors())
}

// searchFixturePreset maps a search preset to the history-workload fixture
// preset that provides accounts/rooms/subscriptions to search against.
func searchFixturePreset(searchPreset string) string {
	switch searchPreset {
	case "search-large":
		return "history-large"
	case "search-medium":
		return "history-medium"
	default:
		return "history-small"
	}
}

func printSearchSummary(w io.Writer, site string, p *SearchPreset, arm string, seed int64, rate int, measured time.Duration, c *SearchCollector) error {
	fmt.Fprintln(w, "=== loadgen search-sustained complete ===")
	fmt.Fprintf(w, "preset: %s    arm: %s    seed: %d    site: %s\n", p.Name, arm, seed, site)
	fmt.Fprintf(w, "target rate: %d req/s    measured window: %s\n", rate, measured)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nper-arm latency (measured window)")
	fmt.Fprintln(tw, "arm\tcount\terrors\tp50\tp95\tp99\tmax")
	arms := c.Arms()
	sort.Strings(arms)
	for _, a := range arms {
		pct := ComputePercentiles(c.Samples(a))
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t%s\t%s\n",
			a, c.Count(a), c.Errors(a), pct.P50, pct.P95, pct.P99, pct.Max)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush latency table: %w", err)
	}
	return nil
}

// writeSearchCSVFile emits one CSVSample row per latency sample, with the arm
// in the Metric column.
func writeSearchCSVFile(path string, c *SearchCollector) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create csv: %w", err)
	}
	defer func() { _ = f.Close() }()
	arms := c.Arms()
	sort.Strings(arms)
	var rows []CSVSample
	for _, arm := range arms {
		for i, d := range c.Samples(arm) {
			rows = append(rows, CSVSample{
				TimestampNs: int64(i),
				RequestID:   strconv.Itoa(i),
				Metric:      arm,
				LatencyNs:   d.Nanoseconds(),
			})
		}
	}
	return WriteCSV(f, rows)
}

// natsSearchRequester is the production SearchRequester. Each call performs
// nats.Conn.RequestWithContext under a per-call timeout context.
type natsSearchRequester struct {
	nc *nats.Conn
}

func newNATSSearchRequester(nc *nats.Conn) *natsSearchRequester {
	return &natsSearchRequester{nc: nc}
}

func (r *natsSearchRequester) Request(ctx context.Context, subj string, data []byte, timeout time.Duration) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	msg, err := r.nc.RequestWithContext(reqCtx, subj, data)
	if err != nil {
		return nil, fmt.Errorf("nats request: %w", err)
	}
	return msg.Data, nil
}
