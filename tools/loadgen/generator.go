package main

import (
	"context"
	"encoding/json"
	"fmt"
	randv2 "math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// InjectMode selects which subject the generator publishes onto.
type InjectMode string

const (
	InjectFrontdoor InjectMode = "frontdoor"
	InjectCanonical InjectMode = "canonical"
)

// Publisher abstracts NATS publishing so tests can inject a recorder.
type Publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

// GeneratorConfig is the parameter bundle for a Generator.
// Preset is *Preset because the struct is large enough that gocritic's
// hugeParam rule would flag the embedded value.
type GeneratorConfig struct {
	Preset         *Preset
	Fixtures       Fixtures
	SiteID         string
	Rate           int
	Inject         InjectMode
	Publisher      Publisher
	Metrics        *Metrics
	Collector      *Collector
	WarmupDeadline time.Time
	// MaxInFlight caps concurrent publishes dispatched from the ticker.
	// Set to 0 to publish serially on the ticker goroutine (legacy behavior,
	// useful for bisection).
	MaxInFlight int
	// ConnIDFor maps a userID to the index of the data connection that
	// will publish on its behalf. Optional; nil means "all publishes go
	// through one connection" — the metric label collapses to "0".
	// Phase 3 §3.6: lets `loadgen_published_total{conn_id}` confirm
	// fan-out across the configured ConnPool.
	ConnIDFor func(userID string) string

	// Ramp, when non-nil, overrides Rate over time per Phase 3 §3.4.
	// Same semantics as the read-scenario tickLoop's Ramp field: the
	// ticker is rebuilt every rateRebuildInterval (1s, or Duration/10
	// for short test ramps). When nil, the loop ticks at fixed Rate.
	Ramp *Ramp
}

// Generator is the open-loop publisher.
type Generator struct {
	cfg     GeneratorConfig
	maxBody string
	// curRate is the rate the ticker is currently set to (rps). Updated
	// by the rebuild goroutine when Ramp is active so publishOne can
	// label each publish with the in-effect rate_bucket.
	curRate atomic.Int64
	// Bug 4: per-Generator sender picker + thread parent pool. Both
	// honor preset config that pre-fix was silently ignored.
	senders *senderPicker
	threads *threadPool
}

// NewGenerator returns a Generator. The `seed` parameter is retained for
// API compatibility but no longer seeds an instance Rand for per-tick
// picks under DistUniform — those use math/rand/v2 globals (S4). The
// Zipf sender picker (Bug 4) does need a seeded source so the head
// of the distribution is reproducible across runs of the same preset;
// it consumes `seed` for that purpose. Fixture seeding still honors
// the same seed via BuildFixtures.
func NewGenerator(cfg *GeneratorConfig, seed int64) *Generator {
	max := cfg.Preset.ContentBytes.Max
	if max <= 0 {
		max = 1
	}
	return &Generator{
		cfg:     *cfg,
		maxBody: strings.Repeat("x", max),
		senders: newSenderPicker(len(cfg.Fixtures.Subscriptions), cfg.Preset.SenderDist, seed),
		threads: newThreadPool(threadPoolCapacity),
	}
}

// threadPoolCapacity bounds the per-Generator thread parent ring buffer.
// 1024 is enough to avoid bias towards the most recent few publishes
// while keeping the working set small.
const threadPoolCapacity = 1024

// drainGracePeriod bounds how long Run waits for in-flight publishes
// to complete after ctx cancels.
const drainGracePeriod = 5 * time.Second

// Run publishes at the configured rate until ctx is cancelled. When
// MaxInFlight > 0, each tick dispatches the publish to a bounded
// goroutine pool so the ticker stays punctual under load; saturation
// (pool full when a tick fires) is recorded as a publish error with
// reason="saturated" rather than silently dropping the tick.
func (g *Generator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 && g.cfg.Ramp == nil {
		return ErrInvalidRate
	}
	rate := g.cfg.Rate
	if g.cfg.Ramp != nil {
		rate = g.cfg.Ramp.RateAt(0)
	}
	g.curRate.Store(int64(rate))
	tick := time.NewTicker(tickInterval(rate))
	defer tick.Stop()

	var rebuild <-chan time.Time
	start := time.Now()
	if g.cfg.Ramp != nil {
		ri := rateRebuildInterval
		if g.cfg.Ramp.Duration > 0 && g.cfg.Ramp.Duration/10 < ri {
			ri = g.cfg.Ramp.Duration / 10
		}
		if ri <= 0 {
			ri = rateRebuildInterval
		}
		rebuildTicker := time.NewTicker(ri)
		defer rebuildTicker.Stop()
		rebuild = rebuildTicker.C
	}
	maybeRebuild := func() {
		if g.cfg.Ramp == nil {
			return
		}
		newRate := g.cfg.Ramp.RateAt(time.Since(start))
		if newRate <= 0 {
			return
		}
		g.curRate.Store(int64(newRate))
		tick.Reset(tickInterval(newRate))
	}

	if g.cfg.MaxInFlight <= 0 {
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				g.publishOne(ctx)
			case <-rebuild:
				maybeRebuild()
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
					g.publishOne(ctx)
				}()
			default:
				g.cfg.Metrics.PublishErrors.WithLabelValues(g.cfg.Preset.Name, "saturated").Inc()
			}
		case <-rebuild:
			maybeRebuild()
		}
	}
}

// S4: removed g.intn/g.float64 — call sites now use math/rand/v2.IntN
// and randv2.Float64 directly (lock-free ChaCha8 globals).

func (g *Generator) publishOne(ctx context.Context) {
	if len(g.cfg.Fixtures.Subscriptions) == 0 {
		return
	}
	// Bug 4 part A: honor SenderDist (pre-fix every preset got uniform).
	// senderPicker maps an index in [0, len(subs)) under the preset's
	// distribution; falls back to uniform when DistZipf isn't set.
	subIdx := g.senders.pick()
	sub := g.cfg.Fixtures.Subscriptions[subIdx]
	content := g.content()
	msgID := idgen.GenerateMessageID()
	publishTime := time.Now()

	// Bug 4 part B: honor ThreadRate. Pull a parent from the recent-pub
	// ring buffer; nil when ThreadRate==0 or the pool is empty (warmup).
	parentID, parentCreated := g.threads.maybeParent(g.cfg.Preset.ThreadRate)

	var (
		subj  string
		data  []byte
		reqID string
		err   error
	)
	switch g.cfg.Inject {
	case InjectCanonical:
		now := time.Now().UTC()
		msg := model.Message{
			ID: msgID, RoomID: sub.RoomID,
			UserID: sub.User.ID, UserAccount: sub.User.Account,
			Content: content, CreatedAt: now,
		}
		if parentID != "" {
			msg.ThreadParentMessageID = parentID
			pc := parentCreated
			msg.ThreadParentMessageCreatedAt = &pc
		}
		evt := model.MessageEvent{
			Message:   msg,
			SiteID:    g.cfg.SiteID,
			Timestamp: now.UnixMilli(),
		}
		data, err = json.Marshal(evt)
		subj = subject.MsgCanonicalCreated(g.cfg.SiteID)
		g.cfg.Collector.RecordPublishBroadcastOnly(msgID, publishTime)
	default:
		reqID = idgen.GenerateRequestID()
		req := model.SendMessageRequest{ID: msgID, Content: content, RequestID: reqID}
		if parentID != "" {
			req.ThreadParentMessageID = parentID
			ms := parentCreated.UnixMilli()
			req.ThreadParentMessageCreatedAt = &ms
		}
		data, err = json.Marshal(req)
		subj = subject.MsgSend(sub.User.Account, sub.RoomID, g.cfg.SiteID)
		g.cfg.Collector.RecordPublish(reqID, msgID, publishTime)
	}
	// Track this publish as a candidate parent for future thread replies.
	// Done before the publish error check so the pool reflects intent
	// even if the broker rejects the publish (the caller's tests do not
	// fail-publish, so the practical effect is just bookkeeping).
	g.threads.add(msgID)
	if err != nil {
		g.cfg.Metrics.PublishErrors.WithLabelValues(g.cfg.Preset.Name, "marshal").Inc()
		return
	}
	if perr := g.cfg.Publisher.Publish(ctx, subj, data); perr != nil {
		g.cfg.Collector.RecordPublishFailed(reqID, msgID)
		g.cfg.Metrics.PublishErrors.WithLabelValues(g.cfg.Preset.Name, "publish").Inc()
		return
	}
	phase := "measured"
	if publishTime.Before(g.cfg.WarmupDeadline) {
		phase = "warmup"
	}
	connID := "0"
	if g.cfg.ConnIDFor != nil {
		connID = g.cfg.ConnIDFor(sub.User.Account)
	}
	g.cfg.Metrics.Published.WithLabelValues(
		g.cfg.Preset.Name, phase, connID, rateBucketLabel(int(g.curRate.Load())),
	).Inc()
}

func (g *Generator) content() string {
	r := g.cfg.Preset.ContentBytes
	size := r.Min
	if r.Max > r.Min {
		size = r.Min + randv2.IntN(r.Max-r.Min+1)
	}
	if size <= 0 {
		size = 1
	}
	body := g.maxBody[:size]
	if g.cfg.Preset.MentionRate > 0 && randv2.Float64() < g.cfg.Preset.MentionRate {
		target := randv2.IntN(g.cfg.Preset.Users)
		body = fmt.Sprintf("@user-%d %s", target, body)
	}
	return body
}
