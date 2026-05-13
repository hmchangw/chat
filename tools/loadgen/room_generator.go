package main

import (
	"context"
	randv2 "math/rand/v2"
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
	Ramp           *Ramp
	Timeout        time.Duration
	// Omission, when non-nil, records coordinated-omission deficits for
	// each tick: the gap between intended dispatch time and actual start
	// (serviced) or drop time (dropped/saturated).
	Omission *OmissionTracker
}

// RoomRPCGenerator drives room-service request/reply RPCs at a steady
// rate, distributing across the five kinds in Preset.RoomMix
// (RoomsList / RoomsGet / MemberList / RoomCreate / MemberAdd). Reuses
// the shared tickLoop helper from Task 10.5; differs from the
// HistoryReadGenerator only in per-tick body.
type RoomRPCGenerator struct {
	cfg RoomRPCConfig
}

// NewRoomRPCGenerator returns a generator. The `seed` parameter is
// retained for API compatibility but no longer seeds an instance Rand
// (S4 — see HistoryReadGenerator).
func NewRoomRPCGenerator(cfg *RoomRPCConfig, seed int64) *RoomRPCGenerator {
	_ = seed
	return &RoomRPCGenerator{
		cfg: *cfg,
	}
}

// Run ticks at the configured rate until ctx is cancelled.
func (g *RoomRPCGenerator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 && g.cfg.Ramp == nil {
		return ErrInvalidRate
	}
	tickLoop(ctx, &tickLoopConfig{
		Rate:           g.cfg.Rate,
		MaxInFlight:    g.cfg.MaxInFlight,
		Metrics:        g.cfg.Metrics,
		Preset:         g.cfg.Preset.Name,
		Scenario:       "room",
		Ramp:           g.cfg.Ramp,
		Omission:       g.cfg.Omission,
		WarmupDeadline: g.cfg.WarmupDeadline,
	}, g.tick)
	return nil
}

// S4: removed g.intn — use math/rand/v2.IntN globals (lock-free).

func (g *RoomRPCGenerator) tick(ctx context.Context) {
	if len(g.cfg.Fixtures.Subscriptions) == 0 || len(g.cfg.Preset.RoomMix) == 0 {
		return
	}
	kind := pickRoomKind(g.cfg.Preset.RoomMix)

	sub := g.cfg.Fixtures.Subscriptions[randv2.IntN(len(g.cfg.Fixtures.Subscriptions))]
	args := roomRequestArgs{
		User:          model.User{Account: sub.User.Account, ID: sub.User.ID, SiteID: g.cfg.SiteID},
		Room:          model.Room{ID: sub.RoomID, SiteID: g.cfg.SiteID},
		SiteID:        g.cfg.SiteID,
		WriteIDPrefix: g.cfg.Preset.WriteIDPrefix,
	}
	if kind == MemberAddKind && len(g.cfg.Fixtures.Users) > 0 {
		args.MemberAccount = g.cfg.Fixtures.Users[randv2.IntN(len(g.cfg.Fixtures.Users))].Account
	}

	phase := "measured"
	if time.Now().Before(g.cfg.WarmupDeadline) {
		phase = "warmup"
	}

	subj, body, err := buildRoomRequest(kind, &args)
	if err != nil {
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "room", roomKindLabel(kind), phase, "marshal",
		).Inc()
		return
	}

	g.cfg.Metrics.Requests.WithLabelValues(
		g.cfg.Preset.Name, "room", roomKindLabel(kind), phase,
	).Inc()

	start := time.Now()
	replyData, err := g.cfg.Requester.Request(ctx, subj, body, g.cfg.Timeout)
	latency := time.Since(start)
	g.cfg.Metrics.RequestLatency.WithLabelValues(
		g.cfg.Preset.Name, "room", roomKindLabel(kind),
	).Observe(latency.Seconds())
	errored := err != nil
	if err != nil {
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "room", roomKindLabel(kind), phase, "request",
		).Inc()
	} else if isAppError(replyData) {
		errored = true
		g.cfg.Metrics.RequestErrors.WithLabelValues(
			g.cfg.Preset.Name, "room", roomKindLabel(kind), phase, "app_error",
		).Inc()
	}
	if g.cfg.Collector != nil {
		g.cfg.Collector.RecordRequest("room", roomKindLabel(kind), phase, latency, errored)
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
