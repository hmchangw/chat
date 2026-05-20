package main

import (
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// summaryInputs bundles the state needed to construct a Summary after gen.Run
// returns. Extracted into a struct to keep buildSummary's parameter list
// manageable.
type summaryInputs struct {
	rt                *Runtime
	rf                *runFlags
	p                 *Preset
	siteID            string
	runID             string
	injectMode        InjectMode
	warmupDeadline    time.Time
	drainTimedOut     bool
	settleOutcome     SettleOutcome
	livenessFailed    bool
	missingReplies    int
	missingBroadcasts int
	collector         *Collector
	samplers          []*ConsumerSampler
	metrics           *Metrics
}

// buildSummary gathers Prometheus metrics, computes run quality, and assembles
// the full Summary struct. It also sets the loadgen_run_quality gauge so the
// metric reflects the final verdict.
//
// Extracted from executeRun to improve cohesion; no behaviour is changed.
func buildSummary(in *summaryInputs) Summary {
	mfs, gerr := in.metrics.Registry.Gather()
	if gerr != nil {
		slog.Warn("metrics gather", "error", gerr)
		mfs = nil
	}
	// Headline summary filters to phase="measured" so warmup errors don't
	// inflate the reported publish-error count shown to the operator.
	publishErrs := gatheredCounterValue(mfs, "loadgen_publish_errors_total", "phase", "measured")
	gkErrs := gatheredCounterLabelPair(mfs, "loadgen_publish_errors_total", "phase", "measured", "reason", "gatekeeper")
	asyncAckErrs := gatheredCounterLabelPair(mfs, "loadgen_publish_errors_total", "phase", "measured", "reason", "async_ack")
	sentWarmup := int(gatheredCounterValue(mfs, "loadgen_published_total", "phase", "warmup"))
	sentMeasured := int(gatheredCounterValue(mfs, "loadgen_published_total", "phase", "measured"))
	sent := sentWarmup + sentMeasured
	// sentQueued is the total number of messages queued for publish (async or
	// sync). For the async path, sentQueued > sentAcked when async_ack errors
	// occurred (acks never received before drain timeout or during the run).
	sentQueued := int64(sent)
	sentAcked := sentQueued - int64(asyncAckErrs)
	// Bug 2: per-scenario measured-window math.
	//   - messaging-pipeline: warmup is carved out of the run window, so
	//     measured = duration - warmup.
	//   - read scenarios with auto-warmup: warmup ran BEFORE runCtx, then
	//     warmupDeadline was reset and the read scenario used the full
	//     rf.Duration. Use time.Since(warmupDeadline) clamped to
	//     [0, rf.Duration] so an early-abort run still reports the fraction
	//     of the window that actually elapsed.
	//   - read scenarios without auto-warmup: warmupDeadline is still in
	//     the future relative to runStart, so the same Since(warmupDeadline)
	//     formula collapses to the post-warmup measured window.
	measured := time.Since(in.warmupDeadline)
	if measured > in.rf.Duration {
		measured = in.rf.Duration
	}
	requestStats := in.collector.RequestStats()
	actualRate := computeActualRate(in.rf.Scenario, in.injectMode, measured,
		in.collector.E1Count(), in.missingReplies, sentMeasured, requestStats)

	// Compute MeasuredP99 from E1 samples (messaging-pipeline) or from
	// request stats (read scenarios). Use the E1 p99 as the primary source;
	// fall back to the first read-scenario p99 when E1 is empty.
	measuredP99 := ComputePercentiles(in.collector.E1Samples()).P99
	if measuredP99 == 0 && len(requestStats) > 0 {
		measuredP99 = requestStats[0].Latency.P99
	}

	// WarmupErrorRate: publish errors during the warmup phase / warmup sends.
	// Now that loadgen_publish_errors_total has a "phase" label, this is exact.
	warmupPublishErrs := gatheredCounterValue(mfs, "loadgen_publish_errors_total", "phase", "warmup")
	var warmupErrorRate float64
	if sentWarmup > 0 {
		warmupErrorRate = warmupPublishErrs / float64(sentWarmup)
		if warmupErrorRate > 1.0 {
			warmupErrorRate = 1.0
		}
	}

	// LivenessFailures: gather from the liveness probe counter.
	livenessFailCount := int(gatheredCounterValue(mfs, "loadgen_liveness_probes_total", "result", "fail"))

	rqi := &RunQualityInputs{
		DrainTimedOut:          in.drainTimedOut,
		MeasuredDuration:       measured,
		AbortP99Sustain:        in.rf.Abort.P99Sustain,
		WarmupErrorRate:        warmupErrorRate,
		SettleOutcome:          in.settleOutcome,
		OmissionP99Serviced:    in.rt.Omission().Quantile(0.99, false),
		MeasuredP99:            measuredP99,
		LivenessFailures:       livenessFailCount,
		LivenessWatcherTripped: in.livenessFailed,
		// TODO Phase 1b: wire AbortWatcherDeafened from abort-watcher
		// saturation signal when that tracking is added.
		AbortWatcherDeafened: false,
		// TODO Phase 1b: wire PeakRPSTimesSustain from
		// resolveAbortPeakRPS × abortMaxSustain.
		PeakRPSTimesSustain: 0,
		// TODO Phase 1b: wire AbortWindowCap from resolvedAbortCap.
		AbortWindowCap: 0,
		// TODO Phase 3.1: wire RAWPollIntervalCoarse from RAW scenario config.
		RAWPollIntervalCoarse: false,
	}
	verdict := EvaluateRunQuality(rqi)

	// Set the loadgen_run_quality gauge: exactly one label value is 1.
	for _, v := range []string{"TRUSTED", "DEGRADED", "UNTRUSTED"} {
		val := 0.0
		if v == verdict.Verdict {
			val = 1.0
		}
		in.metrics.RunQuality.With(prometheus.Labels{"verdict": v}).Set(val)
	}

	// Task 3.12 Fix 2: populate SentByRoomType from the per-room-type counter
	// when DMRatio>0. gatheredCounterLabelPair filters on both (preset, room_type)
	// so runs with different preset names on the same registry don't bleed into
	// each other. When DMRatio==0 the counter is never incremented; the map is
	// omitted (nil) so PrintSummary skips the breakdown line.
	var sentByRoomType map[string]int64
	if in.p.DMRatio > 0 {
		chCount := int64(gatheredCounterLabelPair(mfs,
			"loadgen_published_by_room_type_total", "preset", in.p.Name, "room_type", "channel"))
		dmCount := int64(gatheredCounterLabelPair(mfs,
			"loadgen_published_by_room_type_total", "preset", in.p.Name, "room_type", "dm"))
		sentByRoomType = map[string]int64{"channel": chCount, "dm": dmCount}
	}

	return Summary{
		RunID:               in.runID,
		Preset:              in.p.Name,
		Seed:                in.rf.Seed,
		Site:                in.siteID,
		TargetRate:          in.rf.Rate,
		ActualRate:          actualRate,
		Duration:            in.rf.Duration,
		Warmup:              in.rf.Warmup,
		Inject:              in.rf.Inject,
		Sent:                sent,
		SentMeasured:        sentMeasured,
		SentQueued:          sentQueued,
		SentAcked:           sentAcked,
		DrainTimedOut:       in.drainTimedOut,
		PublishErrors:       int(publishErrs - gkErrs),
		GatekeeperErrors:    int(gkErrs),
		MissingReplies:      in.missingReplies,
		MissingBroadcasts:   in.missingBroadcasts,
		E1:                  ComputePercentiles(in.collector.E1Samples()),
		E2:                  ComputePercentiles(in.collector.E2Samples()),
		E1Count:             in.collector.E1Count(),
		E2Count:             in.collector.E2Count(),
		Consumers:           consumerSnapshots(in.samplers),
		Requests:            requestStats,
		OmissionServicedP99: in.rt.Omission().Quantile(0.99, false),
		OmissionDroppedP99:  in.rt.Omission().Quantile(0.99, true),
		RunQualityVerdict:   verdict,
		SentByRoomType:      sentByRoomType,
	}
}
