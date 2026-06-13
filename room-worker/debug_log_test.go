package main

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/logctx"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
)

type recordingHandler struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *recordingHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }

//nolint:gocritic // slog.Handler mandates the Record value parameter
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r.Clone())
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = nil
}
func (h *recordingHandler) hasLevel(l slog.Level) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.recs {
		if h.recs[i].Level == l {
			return true
		}
	}
	return false
}
func (h *recordingHandler) count(l slog.Level, msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for i := range h.recs {
		if h.recs[i].Level == l && h.recs[i].Message == msg {
			n++
		}
	}
	return n
}
func (h *recordingHandler) has(l slog.Level, msg string) bool { return h.count(l, msg) > 0 }

func installRecorder(t *testing.T) *recordingHandler {
	t.Helper()
	rec := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(logctx.NewHandler(rec)))
	logctx.Configure(logctx.Config{Rate: 1e6, Burst: 1 << 20})
	t.Cleanup(func() {
		slog.SetDefault(prev)
		logctx.Configure(logctx.Config{Rate: 0, Burst: 0})
	})
	return rec
}

func admitRung(rung string) context.Context {
	return logctx.Admit(context.Background(), nats.Header{natsutil.DebugHeader: []string{rung}})
}

func TestPublishSubscriptionUpdates_TraceFanout(t *testing.T) {
	h := &Handler{siteID: "site-a", publish: func(context.Context, string, []byte, string) error { return nil }}
	subs := []*model.Subscription{
		{User: model.SubscriptionUser{ID: "u1", Account: "alice"}},
		{User: model.SubscriptionUser{ID: "u2", Account: "bob"}},
	}
	rec := installRecorder(t)

	t.Run("trace: one delivery line per subscriber + the flow count", func(t *testing.T) {
		rec.reset()
		h.publishSubscriptionUpdates(admitRung("trace"), subs, "req-1")
		assert.Equal(t, 2, rec.count(logctx.LevelTrace, "room-worker subscription delivered"))
		assert.True(t, rec.has(logctx.LevelFlow, "room-worker subscription fan-out"))
	})

	t.Run("flow: count only, no per-subscriber lines", func(t *testing.T) {
		rec.reset()
		h.publishSubscriptionUpdates(admitRung("flow"), subs, "req-1")
		assert.True(t, rec.has(logctx.LevelFlow, "room-worker subscription fan-out"))
		assert.False(t, rec.hasLevel(logctx.LevelTrace))
	})

	t.Run("unadmitted: nothing", func(t *testing.T) {
		rec.reset()
		h.publishSubscriptionUpdates(context.Background(), subs, "req-1")
		assert.False(t, rec.hasLevel(logctx.LevelFlow))
		assert.False(t, rec.hasLevel(logctx.LevelTrace))
	})
}

func TestPublishAsyncJobResult_FlowBreadcrumb(t *testing.T) {
	h := &Handler{siteID: "site-a", publish: func(context.Context, string, []byte, string) error { return nil }}
	rec := installRecorder(t)

	ctx := natsutil.WithRequestID(admitRung("flow"), "req-1")
	h.publishAsyncJobResult(ctx, "alice", "room.create", "room-1", nil)
	assert.True(t, rec.has(logctx.LevelFlow, "room-worker async result"), "two-phase async result emits a flow terminal")
}
