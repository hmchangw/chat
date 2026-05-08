package main

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
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
	WarmupDeadline time.Time
	MaxInFlight    int
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
	cfg   HistoryReadConfig
	rngMu sync.Mutex
	rng   *rand.Rand
}

// NewHistoryReadGenerator returns a generator seeded from `seed`.
func NewHistoryReadGenerator(cfg *HistoryReadConfig, seed int64) *HistoryReadGenerator {
	return &HistoryReadGenerator{
		cfg: *cfg,
		rng: rand.New(rand.NewSource(seed)),
	}
}

// Run ticks at the configured rate until ctx is cancelled. Mirrors the
// dispatch model of the messaging-pipeline Generator: optional bounded
// goroutine pool for in-flight cap, drain on cancel, saturation counted as
// an error rather than silently dropped.
func (g *HistoryReadGenerator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 {
		return fmt.Errorf("rate must be > 0")
	}
	interval := time.Second / time.Duration(g.cfg.Rate)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	if g.cfg.MaxInFlight <= 0 {
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				g.tick(ctx)
			}
		}
	}

	sem := make(chan struct{}, g.cfg.MaxInFlight)
	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(drainGracePeriod):
			}
			return nil
		case <-tick.C:
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer func() {
						<-sem
						wg.Done()
					}()
					g.tick(ctx)
				}()
			default:
				g.cfg.Metrics.RequestErrors.WithLabelValues(
					g.cfg.Preset.Name, "history", "saturated", "saturated",
				).Inc()
			}
		}
	}
}

func (g *HistoryReadGenerator) intn(n int) int {
	g.rngMu.Lock()
	defer g.rngMu.Unlock()
	return g.rng.Intn(n)
}

func (g *HistoryReadGenerator) tick(ctx context.Context) {
	if len(g.cfg.Fixtures.Subscriptions) == 0 || len(g.cfg.Preset.HistoryMix) == 0 {
		return
	}
	g.rngMu.Lock()
	kind := pickHistoryKind(g.rng, g.cfg.Preset.HistoryMix)
	g.rngMu.Unlock()
	sub := g.cfg.Fixtures.Subscriptions[g.intn(len(g.cfg.Fixtures.Subscriptions))]
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
		args.MessageID = g.cfg.MessageIDs[g.intn(len(g.cfg.MessageIDs))]
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
	g.cfg.Metrics.RequestLatency.WithLabelValues(
		g.cfg.Preset.Name, "history", historyKindLabel(kind),
	).Observe(time.Since(start).Seconds())
	if err != nil {
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "history", historyKindLabel(kind), "request",
		).Inc()
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
	WarmupDeadline time.Time
	MaxInFlight    int
	Timeout        time.Duration
}

// SearchReadGenerator drives search-service request/reply RPCs at a steady
// rate, distributing across SearchMessages / SearchRooms per the preset's
// SearchMix weights, and drawing query strings uniformly from
// preset.SearchTokens.
type SearchReadGenerator struct {
	cfg   SearchReadConfig
	rngMu sync.Mutex
	rng   *rand.Rand
}

// NewSearchReadGenerator returns a generator seeded from `seed`.
func NewSearchReadGenerator(cfg *SearchReadConfig, seed int64) *SearchReadGenerator {
	return &SearchReadGenerator{
		cfg: *cfg,
		rng: rand.New(rand.NewSource(seed)),
	}
}

// Run ticks at the configured rate until ctx is cancelled.
func (g *SearchReadGenerator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 {
		return fmt.Errorf("rate must be > 0")
	}
	interval := time.Second / time.Duration(g.cfg.Rate)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	if g.cfg.MaxInFlight <= 0 {
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				g.tick(ctx)
			}
		}
	}

	sem := make(chan struct{}, g.cfg.MaxInFlight)
	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(drainGracePeriod):
			}
			return nil
		case <-tick.C:
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer func() {
						<-sem
						wg.Done()
					}()
					g.tick(ctx)
				}()
			default:
				g.cfg.Metrics.RequestErrors.WithLabelValues(
					g.cfg.Preset.Name, "search", "saturated", "saturated",
				).Inc()
			}
		}
	}
}

func (g *SearchReadGenerator) intn(n int) int {
	g.rngMu.Lock()
	defer g.rngMu.Unlock()
	return g.rng.Intn(n)
}

func (g *SearchReadGenerator) tick(ctx context.Context) {
	if len(g.cfg.Fixtures.Users) == 0 || len(g.cfg.Preset.SearchTokens) == 0 || len(g.cfg.Preset.SearchMix) == 0 {
		return
	}
	g.rngMu.Lock()
	kind := pickSearchKind(g.rng, g.cfg.Preset.SearchMix)
	g.rngMu.Unlock()
	user := g.cfg.Fixtures.Users[g.intn(len(g.cfg.Fixtures.Users))]
	query := g.cfg.Preset.SearchTokens[g.intn(len(g.cfg.Preset.SearchTokens))]
	args := searchRequestArgs{
		User:  user,
		Query: query,
		Scope: "channel",
		Size:  20,
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
	g.cfg.Metrics.RequestLatency.WithLabelValues(
		g.cfg.Preset.Name, "search", searchKindLabel(kind),
	).Observe(time.Since(start).Seconds())
	if err != nil {
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "search", searchKindLabel(kind), "request",
		).Inc()
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
