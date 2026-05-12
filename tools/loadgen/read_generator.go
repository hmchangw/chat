package main

import (
	"context"
	randv2 "math/rand/v2"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

// Requester abstracts a NATS request/reply call for the read scenarios.
// The timeout is forwarded to the underlying nc.RequestWithContext / RequestMsg
// so individual handlers don't need to wire context-with-deadline themselves.
type Requester interface {
	Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error)
}

// HistoryReadConfig is the parameter bundle for a HistoryReadGenerator.
type HistoryReadConfig struct {
	Preset         *Preset
	Fixtures       Fixtures
	SiteID         string
	Rate           int
	Requester      Requester
	Metrics        *Metrics
	Collector      *Collector
	WarmupDeadline time.Time
	MaxInFlight    int
	Ramp           *Ramp
	// MessageIDs are the seed message IDs to sample from for kinds that
	// require one (GetMessageByID, LoadSurroundingMessages,
	// GetThreadMessages). Typically populated from a warm-up phase that
	// publishes via the messaging-pipeline scenario.
	MessageIDs []string
	Timeout    time.Duration
}

// HistoryReadGenerator drives history-service request/reply RPCs at a steady
// rate, distributing across LoadHistory / GetMessageByID / LoadSurrounding /
// GetThreadMessages per the preset's HistoryMix weights.
type HistoryReadGenerator struct {
	cfg HistoryReadConfig
}

// NewHistoryReadGenerator returns a generator. The `seed` parameter is
// retained for API compatibility but no longer seeds an instance Rand —
// per-tick picks now use the lock-free math/rand/v2 globals (S4).
// Fixture seeding (BuildFixtures) still honors the same seed.
func NewHistoryReadGenerator(cfg *HistoryReadConfig, seed int64) *HistoryReadGenerator {
	_ = seed
	return &HistoryReadGenerator{
		cfg: *cfg,
	}
}

// Run ticks at the configured rate until ctx is cancelled. Delegates the
// loop body to the shared tickLoop helper (Cleanup B) so the three read
// generators share dispatch + saturation accounting.
func (g *HistoryReadGenerator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 && g.cfg.Ramp == nil {
		return ErrInvalidRate
	}
	tickLoop(ctx, tickLoopConfig{
		Rate:        g.cfg.Rate,
		MaxInFlight: g.cfg.MaxInFlight,
		Metrics:     g.cfg.Metrics,
		Preset:      g.cfg.Preset.Name,
		Scenario:    "history",
		Ramp:        g.cfg.Ramp,
	}, g.tick)
	return nil
}

// S4: removed g.intn — use math/rand/v2.IntN globals (lock-free).

func (g *HistoryReadGenerator) tick(ctx context.Context) {
	if len(g.cfg.Fixtures.Subscriptions) == 0 || len(g.cfg.Preset.HistoryMix) == 0 {
		return
	}
	kind := pickHistoryKind(g.cfg.Preset.HistoryMix)
	sub := g.cfg.Fixtures.Subscriptions[randv2.IntN(len(g.cfg.Fixtures.Subscriptions))]
	args := historyRequestArgs{
		User: model.User{Account: sub.User.Account, ID: sub.User.ID, SiteID: g.cfg.SiteID},
		Room: model.Room{ID: sub.RoomID, SiteID: g.cfg.SiteID},
	}
	if needsMessageID(kind) {
		if len(g.cfg.MessageIDs) == 0 {
			g.cfg.Metrics.RequestErrors.WithLabelValues(
				g.cfg.Preset.Name, "history", historyKindLabel(kind), "no_message_ids",
			).Inc()
			return
		}
		args.MessageID = g.cfg.MessageIDs[randv2.IntN(len(g.cfg.MessageIDs))]
	}
	subj, body, err := buildHistoryRequest(kind, &args)
	if err != nil {
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "history", historyKindLabel(kind), "marshal",
		).Inc()
		return
	}
	phase := "measured"
	if time.Now().Before(g.cfg.WarmupDeadline) {
		phase = "warmup"
	}
	g.cfg.Metrics.Requests.WithLabelValues(
		g.cfg.Preset.Name, "history", historyKindLabel(kind), phase,
	).Inc()
	start := time.Now()
	_, err = g.cfg.Requester.Request(ctx, subj, body, g.cfg.Timeout)
	latency := time.Since(start)
	g.cfg.Metrics.RequestLatency.WithLabelValues(
		g.cfg.Preset.Name, "history", historyKindLabel(kind),
	).Observe(latency.Seconds())
	if err != nil {
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "history", historyKindLabel(kind), "request",
		).Inc()
	}
	if g.cfg.Collector != nil {
		g.cfg.Collector.RecordRequest("history", historyKindLabel(kind), start, latency, err != nil)
	}
}

func needsMessageID(kind historyRequestKind) bool {
	switch kind {
	case HistoryGetMessageByID, HistoryLoadSurrounding, HistoryGetThreadMessages:
		return true
	default:
		return false
	}
}

func historyKindLabel(kind historyRequestKind) string {
	switch kind {
	case HistoryLoadHistory:
		return "load_history"
	case HistoryGetMessageByID:
		return "get_message_by_id"
	case HistoryLoadSurrounding:
		return "load_surrounding"
	case HistoryGetThreadMessages:
		return "get_thread_messages"
	default:
		return "unknown"
	}
}

// SearchReadConfig is the parameter bundle for a SearchReadGenerator.
type SearchReadConfig struct {
	Preset         *Preset
	Fixtures       Fixtures
	SiteID         string
	Rate           int
	Requester      Requester
	Metrics        *Metrics
	Collector      *Collector
	WarmupDeadline time.Time
	MaxInFlight    int
	Ramp           *Ramp
	Timeout        time.Duration
}

// SearchReadGenerator drives search-service request/reply RPCs at a steady
// rate, distributing across SearchMessages / SearchRooms per the preset's
// SearchMix weights, and drawing query strings uniformly from
// preset.SearchTokens.
type SearchReadGenerator struct {
	cfg SearchReadConfig
}

// NewSearchReadGenerator returns a generator. The `seed` parameter is
// retained for API compatibility but no longer seeds an instance Rand
// (S4 — see HistoryReadGenerator).
func NewSearchReadGenerator(cfg *SearchReadConfig, seed int64) *SearchReadGenerator {
	_ = seed
	return &SearchReadGenerator{
		cfg: *cfg,
	}
}

// Run ticks at the configured rate until ctx is cancelled. Delegates to
// the shared tickLoop helper (Cleanup B).
func (g *SearchReadGenerator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 && g.cfg.Ramp == nil {
		return ErrInvalidRate
	}
	tickLoop(ctx, tickLoopConfig{
		Rate:        g.cfg.Rate,
		MaxInFlight: g.cfg.MaxInFlight,
		Metrics:     g.cfg.Metrics,
		Preset:      g.cfg.Preset.Name,
		Scenario:    "search",
		Ramp:        g.cfg.Ramp,
	}, g.tick)
	return nil
}

// S4: removed g.intn — use math/rand/v2.IntN globals (lock-free).

func (g *SearchReadGenerator) tick(ctx context.Context) {
	if len(g.cfg.Fixtures.Users) == 0 || len(g.cfg.Preset.SearchTokens) == 0 || len(g.cfg.Preset.SearchMix) == 0 {
		return
	}
	kind := pickSearchKind(g.cfg.Preset.SearchMix)
	user := g.cfg.Fixtures.Users[randv2.IntN(len(g.cfg.Fixtures.Users))]
	query := g.cfg.Preset.SearchTokens[randv2.IntN(len(g.cfg.Preset.SearchTokens))]
	size := g.cfg.Preset.SearchSize
	if size <= 0 {
		size = 20
	}
	args := searchRequestArgs{
		User:  user,
		Query: query,
		Scope: g.cfg.Preset.SearchScope,
		Size:  size,
	}
	subj, body, err := buildSearchRequest(kind, &args)
	if err != nil {
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "search", searchKindLabel(kind), "marshal",
		).Inc()
		return
	}
	phase := "measured"
	if time.Now().Before(g.cfg.WarmupDeadline) {
		phase = "warmup"
	}
	g.cfg.Metrics.Requests.WithLabelValues(
		g.cfg.Preset.Name, "search", searchKindLabel(kind), phase,
	).Inc()
	start := time.Now()
	_, err = g.cfg.Requester.Request(ctx, subj, body, g.cfg.Timeout)
	latency := time.Since(start)
	g.cfg.Metrics.RequestLatency.WithLabelValues(
		g.cfg.Preset.Name, "search", searchKindLabel(kind),
	).Observe(latency.Seconds())
	if err != nil {
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "search", searchKindLabel(kind), "request",
		).Inc()
	}
	if g.cfg.Collector != nil {
		g.cfg.Collector.RecordRequest("search", searchKindLabel(kind), start, latency, err != nil)
	}
}

func searchKindLabel(kind searchRequestKind) string {
	switch kind {
	case SearchMessagesKind:
		return "search_messages"
	case SearchRoomsKind:
		return "search_rooms"
	default:
		return "unknown"
	}
}
