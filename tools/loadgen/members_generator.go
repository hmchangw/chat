package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
)

// SustainedMembersConfig is the parameter bundle for an open-loop members
// generator.
type SustainedMembersConfig struct {
	Preset         *MembersPreset
	Fixtures       *Fixtures
	Pools          CandidatePools
	Owners         map[string]string
	SiteID         string
	Rate           int
	UsersPerAdd    int
	Inject         InjectMode
	Shape          Shape
	Publisher      MemberPublisher
	Metrics        *Metrics
	Collector      *MemberCollector
	WarmupDeadline time.Time
	MaxInFlight    int
}

// SustainedMembersGenerator publishes member-add requests at a target rate
// round-robin across the preset's rooms until ctx is cancelled or the pools
// run dry.
type SustainedMembersGenerator struct {
	cfg     SustainedMembersConfig
	mu      sync.Mutex
	pools   map[string][]string
	cursor  int
	roomIDs []string
	rng     *rand.Rand
}

// ErrPoolsExhausted is returned by Run when every room's candidate pool has
// fewer than UsersPerAdd accounts remaining.
var ErrPoolsExhausted = errors.New("candidate pool exhausted on every room — preset's CandidatePool too small for rate * duration * usersPerAdd; re-seed with a larger pool")

// NewSustainedMembersGenerator clones the candidate pools so the input is
// not mutated.
func NewSustainedMembersGenerator(cfg *SustainedMembersConfig, seed int64) *SustainedMembersGenerator {
	pools := make(map[string][]string, len(cfg.Pools))
	roomIDs := make([]string, 0, len(cfg.Fixtures.Rooms))
	for i := range cfg.Fixtures.Rooms {
		r := &cfg.Fixtures.Rooms[i]
		pools[r.ID] = append([]string(nil), cfg.Pools[r.ID]...)
		roomIDs = append(roomIDs, r.ID)
	}
	return &SustainedMembersGenerator{
		cfg:     *cfg,
		pools:   pools,
		roomIDs: roomIDs,
		rng:     rand.New(rand.NewSource(seed)),
	}
}

// Run drives the publish loop. Returns ErrPoolsExhausted if every room runs
// out of candidates before ctx is cancelled.
func (g *SustainedMembersGenerator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 {
		return fmt.Errorf("rate must be > 0")
	}
	if g.cfg.UsersPerAdd <= 0 {
		return fmt.Errorf("usersPerAdd must be > 0")
	}
	interval := time.Second / time.Duration(g.cfg.Rate)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	var sem chan struct{}
	if g.cfg.MaxInFlight > 0 {
		sem = make(chan struct{}, g.cfg.MaxInFlight)
	}
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
			roomID, accounts, ok := g.takeNext()
			if !ok {
				// Drain in-flight before returning so prior publishes complete.
				done := make(chan struct{})
				go func() { wg.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(drainGracePeriod):
				}
				return ErrPoolsExhausted
			}
			if sem == nil {
				g.publishOne(ctx, roomID, accounts)
				continue
			}
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer func() { <-sem; wg.Done() }()
					g.publishOne(ctx, roomID, accounts)
				}()
			default:
				g.cfg.Metrics.MemberPublishErrors.WithLabelValues("saturated").Inc()
				g.giveBack(roomID, accounts)
			}
		}
	}
}

// takeNext rotates through rooms looking for one with at least UsersPerAdd
// candidates. Returns (_, _, false) when every room is below the threshold.
func (g *SustainedMembersGenerator) takeNext() (string, []string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.roomIDs)
	for tried := 0; tried < n; tried++ {
		idx := (g.cursor + tried) % n
		roomID := g.roomIDs[idx]
		if len(g.pools[roomID]) < g.cfg.UsersPerAdd {
			continue
		}
		accounts := g.pools[roomID][:g.cfg.UsersPerAdd]
		g.pools[roomID] = g.pools[roomID][g.cfg.UsersPerAdd:]
		g.cursor = (idx + 1) % n
		return roomID, append([]string(nil), accounts...), true
	}
	return "", nil, false
}

func (g *SustainedMembersGenerator) giveBack(roomID string, accounts []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pools[roomID] = append(accounts, g.pools[roomID]...)
}

func (g *SustainedMembersGenerator) publishOne(ctx context.Context, roomID string, accounts []string) {
	owner := g.cfg.Owners[roomID]
	req := &model.AddMembersRequest{
		RoomID:           roomID,
		Users:            accounts,
		RequesterAccount: owner,
		Timestamp:        time.Now().UTC().UnixMilli(),
	}
	corrID := idgen.GenerateRequestID()
	publishTime := time.Now()
	g.cfg.Collector.RecordPublish(corrID, roomID, accounts, publishTime)

	if err := g.cfg.Publisher.Publish(ctx, owner, roomID, req, corrID); err != nil {
		g.cfg.Collector.RecordPublishFailed(corrID, roomID, accounts)
		g.cfg.Metrics.MemberPublishErrors.WithLabelValues("publish").Inc()
		g.giveBack(roomID, accounts)
		return
	}
	phase := "measured"
	if publishTime.Before(g.cfg.WarmupDeadline) {
		phase = "warmup"
	}
	g.cfg.Metrics.MemberPublished.WithLabelValues(g.cfg.Preset.Name, phase, string(g.cfg.Inject), string(g.cfg.Shape)).Inc()
}

// CapacityMembersConfig parameterizes the per-room sequential growth generator.
type CapacityMembersConfig struct {
	Preset      *MembersPreset
	Fixtures    *Fixtures
	Pools       CandidatePools
	Owners      map[string]string
	SiteID      string
	UsersPerAdd int
	Inject      InjectMode
	Shape       Shape
	TargetSize  int
	MaxRate     int
	Publisher   MemberPublisher
	Metrics     *Metrics
	Collector   *MemberCollector
	E2Timeout   time.Duration
}

// CapacityMembersGenerator drives each room to TargetSize sequentially. Per-
// room loops run concurrently so a slow room does not gate the others.
type CapacityMembersGenerator struct {
	cfg CapacityMembersConfig
}

// NewCapacityMembersGenerator creates a new capacity-mode generator.
func NewCapacityMembersGenerator(cfg *CapacityMembersConfig) *CapacityMembersGenerator {
	return &CapacityMembersGenerator{cfg: *cfg}
}

// Run runs each room until TargetSize or pool exhaustion. Returns nil when
// every room has finished (or ctx cancelled).
func (g *CapacityMembersGenerator) Run(ctx context.Context) error {
	if g.cfg.UsersPerAdd <= 0 {
		return fmt.Errorf("usersPerAdd must be > 0")
	}
	if g.cfg.TargetSize <= 0 {
		return fmt.Errorf("targetSize must be > 0")
	}

	perRoom := make(map[string]chan struct{}, len(g.cfg.Fixtures.Rooms))
	for i := range g.cfg.Fixtures.Rooms {
		perRoom[g.cfg.Fixtures.Rooms[i].ID] = make(chan struct{}, 1)
	}
	g.cfg.Collector.OnBroadcast(func(roomID string, _ []string) {
		if ch, ok := perRoom[roomID]; ok {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	})

	var wg sync.WaitGroup
	for i := range g.cfg.Fixtures.Rooms {
		wg.Add(1)
		room := g.cfg.Fixtures.Rooms[i]
		go func() {
			defer wg.Done()
			g.runRoom(ctx, &room, perRoom[room.ID])
		}()
	}
	wg.Wait()
	return nil
}

func (g *CapacityMembersGenerator) runRoom(ctx context.Context, room *model.Room, ack <-chan struct{}) {
	size := g.cfg.Preset.BaselineSize
	pool := append([]string(nil), g.cfg.Pools[room.ID]...)
	owner := g.cfg.Owners[room.ID]

	var interval time.Duration
	if g.cfg.MaxRate > 0 {
		interval = time.Second / time.Duration(g.cfg.MaxRate)
	}
	var lastSent time.Time

	for size < g.cfg.TargetSize {
		if len(pool) < g.cfg.UsersPerAdd {
			return
		}
		if interval > 0 {
			if delay := interval - time.Since(lastSent); delay > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
			}
		}
		accounts := pool[:g.cfg.UsersPerAdd]
		pool = pool[g.cfg.UsersPerAdd:]

		req := &model.AddMembersRequest{
			RoomID:           room.ID,
			Users:            accounts,
			RequesterAccount: owner,
			Timestamp:        time.Now().UTC().UnixMilli(),
		}
		corrID := idgen.GenerateRequestID()
		publishTime := time.Now()
		lastSent = publishTime
		g.cfg.Collector.RecordPublish(corrID, room.ID, accounts, publishTime)

		if err := g.cfg.Publisher.Publish(ctx, owner, room.ID, req, corrID); err != nil {
			g.cfg.Collector.RecordPublishFailed(corrID, room.ID, accounts)
			g.cfg.Metrics.MemberPublishErrors.WithLabelValues("publish").Inc()
			return
		}
		g.cfg.Metrics.MemberPublished.WithLabelValues(g.cfg.Preset.Name, "measured", string(g.cfg.Inject), string(g.cfg.Shape)).Inc()

		select {
		case <-ack:
			size += g.cfg.UsersPerAdd
			g.cfg.Metrics.MemberRoomSize.WithLabelValues(room.ID).Set(float64(size))
		case <-time.After(g.cfg.E2Timeout):
			g.cfg.Metrics.MemberPublishErrors.WithLabelValues("timeout").Inc()
			return
		case <-ctx.Done():
			return
		}
	}
}
