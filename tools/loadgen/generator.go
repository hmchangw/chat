package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"

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
	Preset    *Preset
	Fixtures  Fixtures
	SiteID    string
	Rate      int
	Inject    InjectMode
	Publisher Publisher
	Metrics   *Metrics
	Collector *Collector
}

// Generator is the open-loop publisher.
type Generator struct {
	cfg GeneratorConfig
	rng *rand.Rand
}

// NewGenerator returns a Generator seeded from `seed`.
func NewGenerator(cfg *GeneratorConfig, seed int64) *Generator {
	return &Generator{cfg: *cfg, rng: rand.New(rand.NewSource(seed))}
}

// Run publishes at the configured rate until ctx is cancelled.
func (g *Generator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 {
		return fmt.Errorf("rate must be > 0")
	}
	interval := time.Second / time.Duration(g.cfg.Rate)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			g.publishOne(ctx)
		}
	}
}

func (g *Generator) publishOne(ctx context.Context) {
	if len(g.cfg.Fixtures.Subscriptions) == 0 {
		return
	}
	subIdx := g.rng.Intn(len(g.cfg.Fixtures.Subscriptions))
	sub := g.cfg.Fixtures.Subscriptions[subIdx]
	content := g.content()
	msgID := uuid.NewString()
	reqID := uuid.NewString()

	var (
		subj string
		data []byte
		err  error
	)
	switch g.cfg.Inject {
	case InjectCanonical:
		now := time.Now().UTC()
		evt := model.MessageEvent{
			Message: model.Message{
				ID: msgID, RoomID: sub.RoomID,
				UserID: sub.User.ID, UserAccount: sub.User.Account,
				Content: content, CreatedAt: now,
			},
			SiteID:    g.cfg.SiteID,
			Timestamp: now.UnixMilli(),
		}
		data, err = json.Marshal(evt)
		subj = subject.MsgCanonicalCreated(g.cfg.SiteID)
	default:
		req := model.SendMessageRequest{ID: msgID, Content: content, RequestID: reqID}
		data, err = json.Marshal(req)
		subj = subject.MsgSend(sub.User.Account, sub.RoomID, g.cfg.SiteID)
	}
	if err != nil {
		g.cfg.Metrics.PublishErrors.WithLabelValues(g.cfg.Preset.Name, "marshal").Inc()
		return
	}
	publishTime := time.Now()
	g.cfg.Collector.RecordPublish(reqID, msgID, publishTime)
	if perr := g.cfg.Publisher.Publish(ctx, subj, data); perr != nil {
		g.cfg.Collector.RecordPublishFailed(reqID, msgID)
		g.cfg.Metrics.PublishErrors.WithLabelValues(g.cfg.Preset.Name, "publish").Inc()
		return
	}
	g.cfg.Metrics.Published.WithLabelValues(g.cfg.Preset.Name).Inc()
}

func (g *Generator) content() string {
	r := g.cfg.Preset.ContentBytes
	size := r.Min
	if r.Max > r.Min {
		size = r.Min + g.rng.Intn(r.Max-r.Min+1)
	}
	if size <= 0 {
		size = 1
	}
	body := strings.Repeat("x", size)
	if g.cfg.Preset.MentionRate > 0 && g.rng.Float64() < g.cfg.Preset.MentionRate {
		target := g.rng.Intn(g.cfg.Preset.Users)
		body = fmt.Sprintf("@user-%d %s", target, body)
	}
	// ThreadRate handling is deferred: fabricating thread-parent fields that
	// pass gatekeeper validation requires tracking previously-published
	// messages, which is not needed for the capacity signal. The preset's
	// ThreadRate is read but unused until thread workloads are exercised.
	return body
}
