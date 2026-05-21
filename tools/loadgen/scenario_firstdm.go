// tools/loadgen/scenario_firstdm.go
//
// first-dm scenario (Phase 3 §3.13): exercises the "first-DM" path for user
// pairs that have NEVER messaged each other. Each tick picks the next
// (userA, userB) from a dedicated loadgen-firstdm-prefixed pool (provisioned
// by `seed --include-first-dm-fixtures`) and observes three sub-lag stages
// along the full create-room → send-message → broadcast path:
//
//	stage=room — publishedAt → observer sees room-canonical create event
//	stage=subs — publishedAt → SubscriptionUpdate seen for BOTH users
//	stage=e2e  — publishedAt → userB sees the DM broadcast event
//
// (An earlier shape included a `stage=persist` measurement keyed off the
// `chat.msg.canonical.{siteID}.created` subject. That stage was removed:
// message-worker consumes the subject silently with no persist-complete
// event, so the lag would have been the loadgen→broker→loadgen self-loop,
// not Cassandra persistence. Measuring real Cassandra persistence requires
// either a Cassandra poll — out of scope for a NATS-only scenario — or a
// new persist-complete signal in message-worker which is a SUT change.)
//
// DM room IDs are computed via idgen.BuildDMRoomID(a, b) per CLAUDE.md §6.
//
// # Wire-up overview
//
// At Run start the scenario installs three wildcard observer subscriptions on
// the shared Subscribers registry:
//
//	chat.room.canonical.{siteID}.create          → "room" stage
//	chat.user.*.event.subscription.update        → "subs" stage (needs both)
//	chat.user.*.event.room                       → "e2e" stage
//
// Each tick:
//  1. pulls the next pair from the pool
//  2. computes dmRoomID = idgen.BuildDMRoomID(userA.ID, userB.ID)
//  3. issues a RoomCreate request from userA (Users=[userB])
//  4. after the room is accepted, publishes the DM canonical message so the
//     e2e stages flow against the same publishedAt anchor
//
// All stage observations are then computed as (observedAt - publishedAt) and
// observed into loadgen_first_dm_lag_seconds{stage}.
//
// # Why no Mongo poll
//
// The original spec sketched a Mongo poll for the subs stage. We observe the
// SubscriptionUpdate NATS event instead — room-worker publishes it under the
// same locking guarantees that gate the Mongo write (subscription doc is
// inserted before the event fires; see room-worker/handler.go finishCreateRoom).
// This keeps the scenario NATS-only and avoids an extra Mongo connection.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// Stage label constants for loadgen_first_dm_lag_seconds{stage}. Keep these
// in sync with metrics.go's FirstDMLag docstring.
const (
	firstDMStageRoom = "room"
	firstDMStageSubs = "subs"
	firstDMStageE2E  = "e2e"
)

// firstDMTrackerCap bounds the per-run tracker's in-flight pair set. At ~5k
// rps publishes × ~1s end-to-end SLO the steady-state set fits in 16k; we
// pick 16384 so a steady-state of 5krps × 3s of slack stays under the cap
// before FIFO eviction kicks in.
const firstDMTrackerCap = 16384

// firstDMStageTimeout bounds how long sendFirstDM waits for the four stages
// to land before giving up. Set to 10s so a stuck pipeline doesn't pile up
// goroutines under sustained load — anything slower than this is a SUT
// issue, not a measurement issue.
const firstDMStageTimeout = 10 * time.Second

// firstDMDefaultBody is the message body used for the synthetic DM publish.
// Tiny constant body — first-dm is about provisioning latency, not
// content-size variance.
const firstDMDefaultBody = "first-dm-loadgen"

// subscriptionUpdateWildcard matches chat.user.{account}.event.subscription.update
// across all accounts. pkg/subject exposes the per-account builder but not
// the wildcard form; we declare it locally rather than introduce a
// cross-service constant just for this scenario.
const subscriptionUpdateWildcard = "chat.user.*.event.subscription.update"

// firstDMScenario exercises the "first-DM" path for user pairs that have
// NEVER messaged each other.
type firstDMScenario struct{}

func (firstDMScenario) Name() string          { return "first-dm" }
func (firstDMScenario) DefaultPreset() string { return "small" }

// NewGenerator constructs the first-dm load generator.
func (firstDMScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &firstDMGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(firstDMScenario{}) }

// firstDMGenerator is the runner produced by firstDMScenario.
type firstDMGenerator struct {
	deps    ScenarioDeps
	rf      *runFlags
	pool    *firstDMPool
	tracker *firstDMTracker

	// sendOverride, when non-nil, replaces sendFirstDM entirely. Used by
	// unit tests to drive the histogram-observation path without a live
	// NATS connection. Production runs leave it nil.
	sendOverride func(ctx context.Context, userA, userB string) map[string]time.Duration
}

// firstDMPool maintains a list of user pairs that have NEVER messaged each
// other. Iterates monotonically — each pair fires exactly once per run,
// unless recycle=true (wraps around the list).
//
// Thread-safe.
type firstDMPool struct {
	mu      sync.Mutex
	pairs   [][2]string
	idx     int
	recycle bool
}

// newFirstDMPool constructs a firstDMPool from the given pairs.
// When recycle is false the pool is exhausted after all pairs have been
// consumed and Next returns (zero, false). When recycle is true the pool
// wraps back to the beginning.
func newFirstDMPool(pairs [][2]string, recycle bool) *firstDMPool {
	return &firstDMPool{pairs: pairs, recycle: recycle}
}

// Next returns the next (userA, userB) pair, or ([2]string{}, false) when the
// pool is exhausted. If recycle is true, wraps to the first pair after
// exhausting the list.
func (p *firstDMPool) Next() ([2]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pairs) == 0 {
		return [2]string{}, false
	}
	if p.idx >= len(p.pairs) {
		if !p.recycle {
			return [2]string{}, false
		}
		p.idx = 0
	}
	pair := p.pairs[p.idx]
	p.idx++
	return pair, true
}

// Run emits first-DM events at the configured rate until ctx is cancelled or
// the pair pool is exhausted (in non-recycle mode).
func (g *firstDMGenerator) Run(ctx context.Context) error {
	pairs := collectFirstDMPairs(g.deps.Fixtures())
	if len(pairs) == 0 {
		slog.Info("first-dm pool empty; skipping",
			"hint", "pass --include-first-dm-fixtures at seed time to provision pairs")
		return nil
	}
	g.pool = newFirstDMPool(pairs, g.rf.FirstDMRecycle)
	g.tracker = newFirstDMTracker()

	// Install observer subscriptions on the run-shared Subscribers registry.
	// Skipped in the unit-test fast path that injects a sendOverride and has
	// no Subscribers available.
	if g.sendOverride == nil {
		if subs := g.deps.Subscribers(); subs != nil {
			adapter := &subscribersAdapter{subs: subs}
			if err := setupFirstDMSubscriptions(adapter, g.tracker, g.deps.SiteID()); err != nil {
				return fmt.Errorf("setup first-dm observers: %w", err)
			}
		} else {
			slog.Warn("first-dm Subscribers registry unavailable; running without stage observers — histogram will be empty")
		}
	}

	if g.rf.FirstDMRecycle {
		slog.Warn("first-dm: --first-dm-recycle enabled",
			"note", "wrapped pairs hit the existing-DM branch in room-service; stage=room will be much shorter than the first pass")
	}

	rate := g.rf.Rate
	if rate <= 0 {
		rate = 5
	}
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	hist := g.deps.Metrics().FirstDMLag
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pair, ok := g.pool.Next()
			if !ok {
				slog.Info("first-dm pool exhausted; exiting cleanly", "consumed", len(pairs))
				return nil
			}
			wg.Add(1)
			go func(p [2]string) {
				defer wg.Done()
				var outcomes map[string]time.Duration
				if g.sendOverride != nil {
					outcomes = g.sendOverride(ctx, p[0], p[1])
				} else {
					outcomes = g.sendFirstDM(ctx, p[0], p[1])
				}
				for stage, lat := range outcomes {
					hist.With(prometheus.Labels{"stage": stage}).Observe(lat.Seconds())
				}
			}(pair)
		}
	}
}

// sendFirstDM issues the RoomCreate request that triggers room-service's DM
// creation path, then publishes the DM canonical message so the e2e stage
// fires under the same publishedAt anchor as the room/subs stages.
//
// Returns the per-stage lag map; stages that don't complete within
// firstDMStageTimeout (e.g. SUT stalled, JetStream consumer wedged) are
// silently omitted so the histogram only carries successful measurements.
func (g *firstDMGenerator) sendFirstDM(ctx context.Context, userA, userB string) map[string]time.Duration {
	dmRoomID := idgen.BuildDMRoomID(userA, userB)
	msgID := idgen.GenerateMessageID()
	publishedAt := time.Now()
	g.tracker.RecordPublish(dmRoomID, msgID, userA, userB, publishedAt)

	siteID := g.deps.SiteID()
	body := buildCreateDMRequestBody(userB)

	// Issue the room-create request. The reply itself is not the room
	// stage timestamp — room-service replies as soon as it publishes the
	// canonical event to JetStream; the observer subscription is what
	// stamps the "room" stage at the actual on-wire moment.
	requestCtx, cancel := context.WithTimeout(ctx, firstDMStageTimeout)
	defer cancel()
	if _, err := g.deps.Requester().Request(requestCtx, subject.RoomCreate(userA, siteID), body, firstDMStageTimeout); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("first-dm room-create request failed", "userA", userA, "userB", userB, "error", err)
		}
		return g.collectLags(dmRoomID)
	}

	// Publish the DM canonical message. The canonical injection path is the
	// most reliable way to drive the e2e stages without depending
	// on the gatekeeper's subscription check (which would race the just-
	// created subscription docs). first-dm is about measuring the room +
	// subs provisioning + broadcast path, not the gatekeeper's auth check.
	canonicalMsg := model.MessageEvent{
		Event: model.EventCreated,
		Message: model.Message{
			ID:          msgID,
			RoomID:      dmRoomID,
			UserAccount: userA,
			Content:     firstDMDefaultBody,
			CreatedAt:   time.Now().UTC(),
		},
		SiteID:    siteID,
		Timestamp: time.Now().UTC().UnixMilli(),
	}
	canonicalData, err := json.Marshal(canonicalMsg)
	if err == nil {
		if perr := g.deps.Publisher().Publish(ctx, subject.MsgCanonicalCreated(siteID), canonicalData); perr != nil {
			slog.Warn("first-dm canonical publish failed", "roomID", dmRoomID, "msgID", msgID, "error", perr)
		}
	}

	// Wait for the tracker to fill in the remaining stages, bounded by
	// firstDMStageTimeout. The tracker is updated asynchronously by the
	// observer subscriptions installed in Run.
	return g.waitForStages(ctx, dmRoomID, publishedAt)
}

// waitForStages polls the tracker until all three stages are recorded or the
// per-iteration timeout elapses. Returns the lag map (possibly partial).
func (g *firstDMGenerator) waitForStages(ctx context.Context, roomID string, publishedAt time.Time) map[string]time.Duration {
	deadline := publishedAt.Add(firstDMStageTimeout)
	// 5ms poll cadence matches the rate at which real first-DM stages
	// typically land in dev (low ms per hop). Smaller than 5ms burns CPU
	// without measurable benefit; larger blurs sub-stage shapes.
	const pollInterval = 5 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		lags := g.collectLags(roomID)
		if len(lags) == 3 {
			return lags
		}
		if time.Now().After(deadline) {
			return lags
		}
		select {
		case <-ctx.Done():
			return lags
		case <-ticker.C:
		}
	}
}

// collectLags returns the current per-stage lag snapshot for roomID.
func (g *firstDMGenerator) collectLags(roomID string) map[string]time.Duration {
	lags, ok := g.tracker.Lags(roomID)
	if !ok {
		return map[string]time.Duration{}
	}
	return lags
}

// buildCreateDMRequestBody constructs the CreateRoomRequest payload that
// targets room-service's DM path: a single counterpart, no name/orgs/channels.
// Returns marshalled JSON ready for Requester.Request.
func buildCreateDMRequestBody(otherAccount string) []byte {
	body, _ := json.Marshal(model.CreateRoomRequest{
		Users: []string{otherAccount},
	})
	return body
}

// collectFirstDMPairs scans the fixture pool for loadgen-firstdm- prefixed
// users and partitions them into pairs by index (first 2 = pair 1, etc.).
func collectFirstDMPairs(f *Fixtures) [][2]string {
	if f == nil {
		return nil
	}
	var users []string
	for i := range f.Users {
		if hasFirstDMPrefix(f.Users[i].ID) {
			users = append(users, f.Users[i].ID)
		}
	}
	pairs := make([][2]string, 0, len(users)/2)
	for i := 0; i+1 < len(users); i += 2 {
		pairs = append(pairs, [2]string{users[i], users[i+1]})
	}
	return pairs
}

// hasFirstDMPrefix returns true if the ID starts with the first-DM fixture prefix.
func hasFirstDMPrefix(id string) bool {
	const prefix = "loadgen-firstdm-"
	if len(id) < len(prefix) {
		return false
	}
	return id[:len(prefix)] == prefix
}

// -------------------- tracker --------------------

// firstDMEntry holds per-pair tracking state. Fields are only accessed under
// the firstDMTracker mutex.
type firstDMEntry struct {
	publishedAt time.Time
	msgID       string
	userA       string
	userB       string
	roomAt      time.Time
	subsA       time.Time
	subsB       time.Time
	e2eAt       time.Time
}

// firstDMTracker is a bounded FIFO map of in-flight DM rooms.
//
// Concurrency: all methods are safe for concurrent use. Observers update the
// tracker from NATS subscription goroutines while the per-iteration waiter
// polls Lags from the dispatch goroutine.
type firstDMTracker struct {
	mu        sync.Mutex
	byRoom    map[string]*firstDMEntry
	roomOrder []string // FIFO eviction
	byMsg     map[string]string
	cap       int
}

func newFirstDMTracker() *firstDMTracker {
	return &firstDMTracker{
		byRoom: make(map[string]*firstDMEntry),
		byMsg:  make(map[string]string),
		cap:    firstDMTrackerCap,
	}
}

// RecordPublish anchors a new (roomID, msgID, userA, userB) tuple at
// publishedAt. Duplicate calls for the same roomID are no-ops so a recycled
// pool doesn't corrupt an in-flight entry.
func (t *firstDMTracker) RecordPublish(roomID, msgID, userA, userB string, publishedAt time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.byRoom[roomID]; exists {
		return
	}
	if len(t.byRoom) >= t.cap {
		evict := t.roomOrder[0]
		t.roomOrder = t.roomOrder[1:]
		if old, ok := t.byRoom[evict]; ok {
			delete(t.byMsg, old.msgID)
		}
		delete(t.byRoom, evict)
	}
	t.byRoom[roomID] = &firstDMEntry{
		publishedAt: publishedAt,
		msgID:       msgID,
		userA:       userA,
		userB:       userB,
	}
	t.byMsg[msgID] = roomID
	t.roomOrder = append(t.roomOrder, roomID)
}

// RecordStage records the observed timestamp for one of
// firstDMStageRoom / firstDMStageSubs / firstDMStageE2E. Unknown rooms
// are silently ignored.
//
// The "subs" stage is recorded via RecordSubsFor instead because it
// requires both userA and userB to be observed.
func (t *firstDMTracker) RecordStage(roomID, stage string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.byRoom[roomID]
	if !ok {
		return
	}
	switch stage {
	case firstDMStageRoom:
		if entry.roomAt.IsZero() {
			entry.roomAt = at
		}
	case firstDMStageE2E:
		if entry.e2eAt.IsZero() {
			entry.e2eAt = at
		}
	}
}

// RecordSubsFor records the SubscriptionUpdate timestamp for one account on
// roomID. The "subs" stage is considered complete once both userA and userB
// have been observed.
//
// Account match is best-effort: room-worker publishes SubscriptionUpdate
// with the subscription's User.Account, but the parser falls back to UserID
// if Account is empty. We accept either form against the pair's recorded
// userA/userB (which are user IDs in the fixture pool, where ID==Account).
func (t *firstDMTracker) RecordSubsFor(roomID, account string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.byRoom[roomID]
	if !ok {
		return
	}
	switch account {
	case entry.userA:
		if entry.subsA.IsZero() {
			entry.subsA = at
		}
	case entry.userB:
		if entry.subsB.IsZero() {
			entry.subsB = at
		}
	}
}

// Lags returns per-stage durations for roomID. Stages that haven't been
// observed yet are omitted. Returns (nil, false) when the room is unknown.
func (t *firstDMTracker) Lags(roomID string) (map[string]time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.byRoom[roomID]
	if !ok {
		return nil, false
	}
	out := make(map[string]time.Duration, 4)
	if !entry.roomAt.IsZero() {
		out[firstDMStageRoom] = entry.roomAt.Sub(entry.publishedAt)
	}
	if !entry.subsA.IsZero() && !entry.subsB.IsZero() {
		latest := entry.subsA
		if entry.subsB.After(latest) {
			latest = entry.subsB
		}
		out[firstDMStageSubs] = latest.Sub(entry.publishedAt)
	}
	if !entry.e2eAt.IsZero() {
		out[firstDMStageE2E] = entry.e2eAt.Sub(entry.publishedAt)
	}
	return out, true
}

// RoomIDForMessage maps a published message ID back to its DM room ID. Used
// by the e2e observer, whose RoomEvent envelope carries the msgID but no
// roomID.
func (t *firstDMTracker) RoomIDForMessage(msgID string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	roomID, ok := t.byMsg[msgID]
	return roomID, ok
}

// Size returns the number of tracked rooms. Primarily for tests.
func (t *firstDMTracker) Size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byRoom)
}

// -------------------- payload parsers --------------------

// parseRoomCreatePayload extracts the RoomID from a CreateRoomRequest
// canonical event payload.
func parseRoomCreatePayload(data []byte) (string, bool) {
	var req struct {
		RoomID string `json:"roomId"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return "", false
	}
	if req.RoomID == "" {
		return "", false
	}
	return req.RoomID, true
}

// parseSubscriptionUpdatePayload extracts (roomID, account) from a
// SubscriptionUpdateEvent. Falls back to UserID when Subscription.User.Account
// is empty.
func parseSubscriptionUpdatePayload(data []byte) (roomID, account string, ok bool) {
	var evt struct {
		UserID       string `json:"userId"`
		Subscription struct {
			RoomID string `json:"roomId"`
			User   struct {
				ID      string `json:"id"`
				Account string `json:"account"`
			} `json:"u"`
		} `json:"subscription"`
	}
	if err := json.Unmarshal(data, &evt); err != nil {
		return "", "", false
	}
	roomID = evt.Subscription.RoomID
	if roomID == "" {
		return "", "", false
	}
	account = evt.Subscription.User.Account
	if account == "" {
		account = evt.UserID
	}
	if account == "" {
		account = evt.Subscription.User.ID
	}
	if account == "" {
		return "", "", false
	}
	return roomID, account, true
}

// parseUserRoomEventPayload extracts the message ID from a RoomEvent envelope
// delivered on chat.user.{account}.event.room. Returns ok=false when the
// payload is malformed or lacks a message ID.
func parseUserRoomEventPayload(data []byte) (string, bool) {
	var evt struct {
		Type    string `json:"type"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &evt); err != nil {
		return "", false
	}
	if evt.Message.ID == "" {
		return "", false
	}
	return evt.Message.ID, true
}

// -------------------- subscription wire-up --------------------

// firstDMSubscriber is the minimal surface the scenario needs from the
// Subscribers registry, narrowed for unit-test substitutability. The
// production adapter wraps *Subscribers; tests provide a recording fake.
type firstDMSubscriber interface {
	SubscribeData(subj string, handler func([]byte)) error
}

// subscribersAdapter bridges *Subscribers (whose Subscribe takes a
// *nats.Msg handler) to firstDMSubscriber (which surfaces raw bytes).
type subscribersAdapter struct {
	subs *Subscribers
}

func (a *subscribersAdapter) SubscribeData(subj string, handler func([]byte)) error {
	_, err := a.subs.Subscribe(subj, func(m *nats.Msg) {
		// Defensive: drop nil/empty payloads silently so a stray health
		// check publish never panics the handler.
		if m == nil || len(m.Data) == 0 {
			return
		}
		handler(m.Data)
	})
	return err
}

// setupFirstDMSubscriptions installs the four observer subscriptions on subs
// that feed the per-stage events into tracker. siteID scopes the room-
// canonical and message-canonical subjects to the local site.
func setupFirstDMSubscriptions(subs firstDMSubscriber, tracker *firstDMTracker, siteID string) error {
	if err := subs.SubscribeData(subject.RoomCanonical(siteID, "create"), func(data []byte) {
		roomID, ok := parseRoomCreatePayload(data)
		if !ok {
			return
		}
		tracker.RecordStage(roomID, firstDMStageRoom, time.Now())
	}); err != nil {
		return fmt.Errorf("subscribe room-canonical: %w", err)
	}
	if err := subs.SubscribeData(subscriptionUpdateWildcard, func(data []byte) {
		roomID, account, ok := parseSubscriptionUpdatePayload(data)
		if !ok {
			return
		}
		tracker.RecordSubsFor(roomID, account, time.Now())
	}); err != nil {
		return fmt.Errorf("subscribe subscription-update: %w", err)
	}
	// Note: a previous implementation subscribed to
	// `chat.msg.canonical.{siteID}.created` to measure a "persist" stage,
	// but message-worker consumes that subject silently and emits no
	// persist-complete event — the resulting lag was the loadgen→broker→
	// loadgen self-loop, not Cassandra persistence. Removed in favour of
	// the three stages that do have real signals: room / subs / e2e.
	// (The published canonical event still happens, see publish loop below,
	// because broadcast-worker needs it to drive the e2e stage.)
	if err := subs.SubscribeData(subject.UserRoomEventWildcard(), func(data []byte) {
		msgID, ok := parseUserRoomEventPayload(data)
		if !ok {
			return
		}
		roomID, ok := tracker.RoomIDForMessage(msgID)
		if !ok {
			return
		}
		tracker.RecordStage(roomID, firstDMStageE2E, time.Now())
	}); err != nil {
		return fmt.Errorf("subscribe user-room-event: %w", err)
	}
	return nil
}
