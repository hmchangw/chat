package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
)

// renderConsole writes a human-readable step-by-step table plus the ANSWER
// line (largest passing N) to w.
func renderConsole(w io.Writer, results []StepResult) {
	fmt.Fprintln(w, "N        p50    p95    p99    err%    worst-pending-delta             verdict")
	var lastPass int
	for i := range results {
		r := &results[i]
		var verdict string
		switch {
		case r.Inconclusive:
			verdict = "INCONCLUSIVE"
		case r.Tripped:
			verdict = "TRIP"
		default:
			verdict = "PASS"
			lastPass = r.N
		}
		worst := worstPending(r.ConsumerPending)
		fmt.Fprintf(w, "%-8d %-6.0f %-6.0f %-6.0f %-7.2f%% %-30s %s\n",
			r.N, r.P50LatencyMs, r.P95LatencyMs, r.P99LatencyMs,
			r.ErrorRate*100, worst, verdict)
		if r.Tripped && len(r.TrippedReasons) > 0 {
			fmt.Fprintf(w, "    reasons: %s\n", joinReasons(r.TrippedReasons))
		}
	}
	fmt.Fprintln(w)
	if lastPass > 0 {
		fmt.Fprintf(w, "ANSWER: N = %d (last passing step)\n", lastPass)
		for i := range results {
			if results[i].Tripped {
				fmt.Fprintf(w, "        Next limit: %s\n", joinReasons(results[i].TrippedReasons))
				break
			}
		}
	} else {
		fmt.Fprintln(w, "ANSWER: no step passed")
	}
}

func worstPending(m map[string]ConsumerPendingDelta) string {
	var worstName string
	var worstDelta int64
	for name, d := range m {
		if d.Delta > worstDelta {
			worstDelta = d.Delta
			worstName = name
		}
	}
	if worstName == "" {
		return "-"
	}
	return fmt.Sprintf("%s +%d", worstName, worstDelta)
}

func joinReasons(rs []string) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += "; "
		}
		out += r
	}
	return out
}

// writeDailyCSV writes one row per StepResult, sorted ascending by N.
func writeDailyCSV(path string, results []StepResult) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create csv: %w", err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{
		"n", "started_at", "p50_ms", "p95_ms", "p99_ms",
		"error_rate", "attempted_ops", "failed_ops",
		"worst_durable", "worst_pending_delta",
		"tripped", "inconclusive", "tripped_reasons",
	}); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}
	rs := make([]StepResult, len(results))
	copy(rs, results)
	sort.Slice(rs, func(i, j int) bool { return rs[i].N < rs[j].N })

	for i := range rs {
		r := &rs[i]
		worstName, worstDelta := "", int64(0)
		for name, d := range r.ConsumerPending {
			if d.Delta > worstDelta {
				worstDelta, worstName = d.Delta, name
			}
		}
		if err := w.Write([]string{
			strconv.Itoa(r.N),
			r.StartedAt.UTC().Format("2006-01-02T15:04:05Z"),
			fmt.Sprintf("%.0f", r.P50LatencyMs),
			fmt.Sprintf("%.0f", r.P95LatencyMs),
			fmt.Sprintf("%.0f", r.P99LatencyMs),
			fmt.Sprintf("%.6f", r.ErrorRate),
			strconv.FormatInt(r.AttemptedOps, 10),
			strconv.FormatInt(r.FailedOps, 10),
			worstName,
			strconv.FormatInt(worstDelta, 10),
			strconv.FormatBool(r.Tripped),
			strconv.FormatBool(r.Inconclusive),
			joinReasons(r.TrippedReasons),
		}); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}
	return nil
}
