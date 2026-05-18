package main

import (
	"context"
	"fmt"
	"time"
)

// autoWarmupConfig is the parameter bundle for runAutoWarmup. The
// publisher must be the messaging-pipeline frontdoor publisher; the
// collector is the same one that will later be passed to the read
// generator (so the seed-discard window in DiscardBefore lands on
// the correct samples).
type autoWarmupConfig struct {
	Preset    *Preset
	Fixtures  Fixtures
	SiteID    string
	Rate      int
	Publisher Publisher
	Metrics   *Metrics
	Collector *Collector
	Duration  time.Duration
	// Seed is the RNG seed for the warm-up Generator. Honor the user's
	// `--seed` flag so two runs with the same `--seed` value emit the
	// same message-ID pool. Pre-fix, this used `time.Now().UnixNano()`,
	// breaking the determinism guarantee the rest of the harness honors.
	Seed int64
}

// runAutoWarmup runs a brief messaging-pipeline phase to populate the
// Collector's message-ID pool. Returns the harvested IDs; callers pass
// them to HistoryReadConfig.MessageIDs.
//
// The warm-up uses InjectFrontdoor (the same path the messaging-pipeline
// scenario uses by default) so the published messages flow through
// gatekeeper → MESSAGES_CANONICAL → message-worker (Cassandra writes).
// Once the warm-up window elapses, the message IDs are real and exist
// in Cassandra, so subsequent GetMessageByID / LoadSurroundingMessages /
// GetThreadMessages calls return data instead of "not found".
func runAutoWarmup(ctx context.Context, cfg *autoWarmupConfig) ([]string, error) {
	if cfg.Duration <= 0 {
		return nil, fmt.Errorf("warm-up duration must be > 0")
	}
	if cfg.Rate <= 0 {
		return nil, ErrInvalidRate
	}
	// WarmupDeadline = far future means every publish in this auto-warmup
	// phase is correctly labelled phase="warmup" in loadgen_published_total
	// (F9 fix). Pre-fix this was set to time.Now() so warmup publishes
	// were tagged "measured", inflating the read scenario's measured
	// publish count by the warmup share.
	farFuture := time.Now().Add(24 * time.Hour)
	gen := NewGenerator(&GeneratorConfig{
		Preset:         cfg.Preset,
		Fixtures:       cfg.Fixtures,
		SiteID:         cfg.SiteID,
		Rate:           cfg.Rate,
		Inject:         InjectFrontdoor,
		Publisher:      cfg.Publisher,
		Metrics:        cfg.Metrics,
		Collector:      cfg.Collector,
		WarmupDeadline: farFuture,
	}, cfg.Seed)

	warmupCtx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()
	if err := gen.Run(warmupCtx); err != nil {
		return nil, fmt.Errorf("auto-warmup generator: %w", err)
	}
	return cfg.Collector.MessageIDs(), nil
}
