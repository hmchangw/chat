package main

import (
	"context"
	"fmt"
	"time"
)

// needsAutoWarmup reports whether the chosen scenario requires a brief
// messaging-pipeline phase to populate the message-ID pool. Today only
// `history-read` does, and only when the configured HistoryMix includes
// at least one kind that needs an ID
// (GetMessageByID / LoadSurroundingMessages / GetThreadMessages).
//
// Phase 3 §3.1: auto warm-up is opt-out via --auto-warmup=false; this
// helper only answers the "is it useful?" question, not the "is it
// enabled?" question. Callers must AND the result with the user's
// opt-out flag.
func needsAutoWarmup(scenario string, p *Preset) bool {
	if scenario != "history-read" {
		return false
	}
	for kind := range p.HistoryMix {
		if needsMessageID(kind) {
			return true
		}
	}
	return false
}

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
	gen := NewGenerator(&GeneratorConfig{
		Preset:    cfg.Preset,
		Fixtures:  cfg.Fixtures,
		SiteID:    cfg.SiteID,
		Rate:      cfg.Rate,
		Inject:    InjectFrontdoor,
		Publisher: cfg.Publisher,
		Metrics:   cfg.Metrics,
		Collector: cfg.Collector,
		// WarmupDeadline = now means every published sample is "measured"
		// from the messaging-pipeline generator's perspective. The
		// surrounding read generator owns its own warmup window via the
		// outer Collector.DiscardBefore call.
		WarmupDeadline: time.Now(),
	}, time.Now().UnixNano())

	warmupCtx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()
	if err := gen.Run(warmupCtx); err != nil {
		return nil, fmt.Errorf("auto-warmup generator: %w", err)
	}
	return cfg.Collector.MessageIDs(), nil
}
