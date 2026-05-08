package main

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

// RoomRPCConfig is the parameter bundle for a RoomRPCGenerator.
type RoomRPCConfig struct {
	Preset         *Preset
	Fixtures       Fixtures
	SiteID         string
	Rate           int
	Requester      Requester
	Metrics        *Metrics
	Collector      *Collector
	WarmupDeadline time.Time
	MaxInFlight    int
	Timeout        time.Duration
}

// RoomRPCGenerator drives room-service request/reply RPCs at a steady
// rate, distributing across the five kinds in Preset.RoomMix
// (RoomsList / RoomsGet / MemberList / RoomCreate / MemberAdd). Reuses
// the shared tickLoop helper from Task 10.5; differs from the
// HistoryReadGenerator only in per-tick body.
type RoomRPCGenerator struct {
	cfg   RoomRPCConfig
	rngMu sync.Mutex
	rng   *rand.Rand
}

// NewRoomRPCGenerator returns a generator seeded from `seed`.
func NewRoomRPCGenerator(cfg *RoomRPCConfig, seed int64) *RoomRPCGenerator {
	return &RoomRPCGenerator{
		cfg: *cfg,
		rng: rand.New(rand.NewSource(seed)),
	}
}

// Run ticks at the configured rate until ctx is cancelled.
func (g *RoomRPCGenerator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 {
		return ErrInvalidRate
	}
	tickLoop(ctx, tickLoopConfig{
		Rate:        g.cfg.Rate,
		MaxInFlight: g.cfg.MaxInFlight,
		Metrics:     g.cfg.Metrics,
		Preset:      g.cfg.Preset.Name,
		Scenario:    "room",
	}, g.tick)
	return nil
}

func (g *RoomRPCGenerator) intn(n int) int {
	g.rngMu.Lock()
	defer g.rngMu.Unlock()
	return g.rng.Intn(n)
}

func (g *RoomRPCGenerator) tick(ctx context.Context) {
	if len(g.cfg.Fixtures.Subscriptions) == 0 || len(g.cfg.Preset.RoomMix) == 0 {
		return
	}
	g.rngMu.Lock()
	kind := pickRoomKind(g.rng, g.cfg.Preset.RoomMix)
	g.rngMu.Unlock()

	sub := g.cfg.Fixtures.Subscriptions[g.intn(len(g.cfg.Fixtures.Subscriptions))]
	args := roomRequestArgs{
		User:          model.User{Account: sub.User.Account, ID: sub.User.ID, SiteID: g.cfg.SiteID},
		Room:          model.Room{ID: sub.RoomID, SiteID: g.cfg.SiteID},
		SiteID:        g.cfg.SiteID,
		WriteIDPrefix: g.cfg.Preset.WriteIDPrefix,
	}
	if kind == MemberAddKind && len(g.cfg.Fixtures.Users) > 0 {
		args.MemberAccount = g.cfg.Fixtures.Users[g.intn(len(g.cfg.Fixtures.Users))].Account
	}

	subj, body, err := buildRoomRequest(kind, &args)
	if err != nil {
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "room", roomKindLabel(kind), "marshal",
		).Inc()
		return
	}

	phase := "measured"
	if time.Now().Before(g.cfg.WarmupDeadline) {
		phase = "warmup"
	}
	g.cfg.Metrics.Requests.WithLabelValues(
		g.cfg.Preset.Name, "room", roomKindLabel(kind), phase,
	).Inc()

	start := time.Now()
	_, err = g.cfg.Requester.Request(ctx, subj, body, g.cfg.Timeout)
	latency := time.Since(start)
	g.cfg.Metrics.RequestLatency.WithLabelValues(
		g.cfg.Preset.Name, "room", roomKindLabel(kind),
	).Observe(latency.Seconds())
	if err != nil {
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "room", roomKindLabel(kind), "request",
		).Inc()
	}
	if g.cfg.Collector != nil {
		g.cfg.Collector.RecordRequest("room", roomKindLabel(kind), start, latency, err != nil)
	}
}

func roomKindLabel(kind roomRequestKind) string {
	switch kind {
	case RoomsListKind:
		return "rooms_list"
	case RoomsGetKind:
		return "rooms_get"
	case MemberListKind:
		return "member_list"
	case RoomCreateKind:
		return "room_create"
	case MemberAddKind:
		return "member_add"
	default:
		return "unknown"
	}
}
