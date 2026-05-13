package main

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// RunQualityInputs collects every signal that EvaluateRunQuality needs
// to classify the run. Callers populate it after the measured window closes
// and before PrintSummary is called.
type RunQualityInputs struct {
	// DrainTimedOut is true when the async JetStream publish drain exceeded
	// asyncDrainTimeout. An incomplete drain means some ack failures were not
	// captured before the report was emitted.
	DrainTimedOut bool

	// MeasuredDuration is the length of the post-warmup measured window.
	// Derived from time.Since(warmupDeadline), capped to rf.Duration.
	MeasuredDuration time.Duration

	// AbortP99Sustain is the --abort-p99-sustain flag value. The measured
	// window must be at least this long for the abort watcher to have had
	// a meaningful observation period.
	AbortP99Sustain time.Duration

	// WarmupErrorRate is errors/total for warmup-phase publishes.
	// 0 means no errors (or no warmup). Values >0.20 are UNTRUSTED;
	// values 0.05–0.20 are DEGRADED.
	WarmupErrorRate float64

	// SettleOutcome is the result of the settle phase (if one ran).
	// A zero-value SettleOutcome with empty Probes is a no-op (no settle phase).
	SettleOutcome SettleOutcome

	// OmissionP99Serviced is the p99 coordinated-omission dispatch deficit
	// for serviced (not dropped) ticks. Excess above OmissionBudgetMs (or
	// the auto-scaled 25%-of-P99 budget) degrades the verdict.
	OmissionP99Serviced time.Duration

	// OmissionBudgetMs overrides the auto-scaled omission budget.
	// When 0, the budget is auto-scaled to 25% of MeasuredP99.
	OmissionBudgetMs int

	// MeasuredP99 is the p99 latency over the measured window (used to
	// auto-scale the omission budget when OmissionBudgetMs == 0).
	MeasuredP99 time.Duration

	// AbortWatcherDeafened is true when the abort watcher's ring buffer
	// cap was hit during the run (sample retention compressed below the
	// sustain window).
	// TODO Phase 1b: wire from abort-watcher saturation signal.
	AbortWatcherDeafened bool

	// PeakRPSTimesSustain is peak_rps × max_sustain_seconds. Used together
	// with AbortWindowCap to assess how severely the abort watcher was
	// deafened.
	// TODO Phase 1b: wire from resolveAbortPeakRPS × abortMaxSustain.
	PeakRPSTimesSustain int

	// AbortWindowCap is the effective abort ring-buffer cap (resolvedAbortCap).
	// TODO Phase 1b: pass resolvedAbortCap from executeRun.
	AbortWindowCap int

	// LivenessFailures is the count of individual liveness probe failures
	// (not necessarily consecutive) during the run.
	// Distinct from LivenessWatcherTripped, which means the watcher fired.
	LivenessFailures int

	// LivenessWatcherTripped is true when the liveness watcher accumulated
	// enough consecutive failures to cancel the run (exit code 3).
	LivenessWatcherTripped bool

	// RAWPollIntervalCoarse is true when the RAW scenario's poll interval
	// is too coarse relative to the measured p50, biasing latency estimates.
	// TODO Phase 3.1: wire from the RAW scenario configuration check.
	RAWPollIntervalCoarse bool
}

// RunQualityVerdict is the output of EvaluateRunQuality.
type RunQualityVerdict struct {
	Verdict string   // TRUSTED | DEGRADED | UNTRUSTED
	Issues  []string // human-readable explanations for non-TRUSTED verdicts
}

// EvaluateRunQuality classifies a run as TRUSTED, DEGRADED, or UNTRUSTED
// based on the signals in in. UNTRUSTED means the reported numbers should
// not be used to draw conclusions about the SUT. DEGRADED means the numbers
// are usable but with caveats. TRUSTED means the run was clean.
//
// UNTRUSTED rules take priority: if any UNTRUSTED condition fires, the
// verdict is UNTRUSTED even when DEGRADED conditions also apply. All issues
// (both UNTRUSTED-tier and DEGRADED-tier) are included in the output for
// visibility.
func EvaluateRunQuality(in *RunQualityInputs) RunQualityVerdict {
	var degraded, untrusted []string

	// -------------------------------------------------------------------------
	// UNTRUSTED rules
	// -------------------------------------------------------------------------

	if in.DrainTimedOut {
		untrusted = append(untrusted,
			"async drain timed out → see USAGE.md#drain-timeout")
	}

	if in.MeasuredDuration < in.AbortP99Sustain {
		untrusted = append(untrusted,
			fmt.Sprintf("measured window %v shorter than abort-p99-sustain %v → see USAGE.md#measured-too-short",
				in.MeasuredDuration, in.AbortP99Sustain))
	}

	if in.WarmupErrorRate > 0.20 {
		untrusted = append(untrusted,
			fmt.Sprintf("warmup error rate %.0f%% > 20%% → see USAGE.md#warmup-errors",
				in.WarmupErrorRate*100))
	}

	// Settle failure only fires when a settle phase was actually configured
	// (len(Probes) > 0). A zero-value SettleOutcome (no probes) is a no-op.
	if !in.SettleOutcome.AllSucceeded && len(in.SettleOutcome.Probes) > 0 {
		untrusted = append(untrusted,
			fmt.Sprintf("settle phase incomplete: %d/%d probes succeeded → see USAGE.md#settle-incomplete",
				in.SettleOutcome.Succeeded, len(in.SettleOutcome.Probes)))
	}

	if in.AbortWatcherDeafened && in.AbortWindowCap > 0 && in.PeakRPSTimesSustain > 2*in.AbortWindowCap {
		untrusted = append(untrusted,
			"abort watcher deafened by sample cap (ratio >2×) → see USAGE.md#abort-watcher-deafened")
	}

	// -------------------------------------------------------------------------
	// DEGRADED rules
	// -------------------------------------------------------------------------

	if in.WarmupErrorRate > 0.05 && in.WarmupErrorRate <= 0.20 {
		degraded = append(degraded,
			fmt.Sprintf("warmup error rate %.0f%% (5–20%%)", in.WarmupErrorRate*100))
	}

	if in.AbortWatcherDeafened && in.AbortWindowCap > 0 && in.PeakRPSTimesSustain <= 2*in.AbortWindowCap {
		degraded = append(degraded,
			"abort watcher deafened by sample cap (partial)")
	}

	budgetMs := in.OmissionBudgetMs
	if budgetMs == 0 && in.MeasuredP99 > 0 {
		budgetMs = int(in.MeasuredP99.Milliseconds() / 4) // 25% of measured p99
	}
	if budgetMs > 0 && in.OmissionP99Serviced > time.Duration(budgetMs)*time.Millisecond {
		degraded = append(degraded,
			fmt.Sprintf("omission p99 %v exceeds budget %dms → see USAGE.md#omission-budget",
				in.OmissionP99Serviced, budgetMs))
	}

	if in.LivenessFailures > 0 && !in.LivenessWatcherTripped {
		degraded = append(degraded,
			fmt.Sprintf("liveness probe failed %d times without watcher trip", in.LivenessFailures))
	}

	if in.RAWPollIntervalCoarse {
		degraded = append(degraded,
			"RAW poll-interval too coarse vs measured p50 → see USAGE.md#raw-poll-bias")
	}

	// -------------------------------------------------------------------------
	// Verdict
	// -------------------------------------------------------------------------

	if len(untrusted) > 0 {
		return RunQualityVerdict{
			Verdict: "UNTRUSTED",
			Issues:  append(untrusted, degraded...),
		}
	}
	if len(degraded) > 0 {
		return RunQualityVerdict{Verdict: "DEGRADED", Issues: degraded}
	}
	return RunQualityVerdict{Verdict: "TRUSTED"}
}

// PrintRunQuality writes the RUN QUALITY banner to out. Called at the top of
// the summary so the verdict is the first thing the operator sees.
func PrintRunQuality(out io.Writer, v RunQualityVerdict) {
	sep := strings.Repeat("=", 32)
	header := fmt.Sprintf("%s RUN QUALITY: %s %s", sep, v.Verdict, sep)
	fmt.Fprintln(out, header)
	for _, iss := range v.Issues {
		fmt.Fprintf(out, "  - %s\n", iss)
	}
	fmt.Fprintln(out, strings.Repeat("=", len(header)))
}
