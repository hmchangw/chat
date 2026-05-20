package cachestats

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureHandler stores each slog record it sees as a JSON line so
// tests can decode and assert without coupling to slog's text format.
type captureHandler struct {
	mu    sync.Mutex
	lines []map[string]any
	inner slog.Handler
}

func newCaptureHandler() (*captureHandler, *slog.Logger) {
	var buf bytes.Buffer
	jh := slog.NewJSONHandler(&buf, nil)
	h := &captureHandler{inner: jh}
	return h, slog.New(h)
}

func (h *captureHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error { //nolint:gocritic // slog.Handler interface requires slog.Record by value
	var buf bytes.Buffer
	if err := slog.NewJSONHandler(&buf, nil).Handle(ctx, r); err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		return err
	}
	h.mu.Lock()
	h.lines = append(h.lines, m)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	return &captureHandler{inner: h.inner.WithGroup(name)}
}

func (h *captureHandler) snapshot() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]map[string]any, len(h.lines))
	copy(out, h.lines)
	return out
}

func TestStartLogger_EmitsLinePerCachePerTick(t *testing.T) {
	s, _ := newStats(t)
	r1 := s.Register("subscription", nil)
	r2 := s.Register("roommeta", func() int { return 7 })

	r1.Hit()
	r1.Hit()
	r1.Miss()
	r2.Hit()

	cap, logger := newCaptureHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := s.StartLogger(ctx, 10*time.Millisecond, logger)
	defer stop()

	// Wait for at least one tick to fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(cap.snapshot()) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()

	lines := cap.snapshot()
	require.GreaterOrEqual(t, len(lines), 2, "expected at least one line per cache")

	// Find the most recent line for each cache.
	byCache := map[string]map[string]any{}
	for _, l := range lines {
		byCache[l["cache"].(string)] = l
	}

	sub := byCache["subscription"]
	require.NotNil(t, sub)
	assert.Equal(t, "cache stats", sub["msg"])
	assert.EqualValues(t, 2, sub["hits"])
	assert.EqualValues(t, 1, sub["misses"])
	assert.InDelta(t, 0.6666, sub["hit_rate"].(float64), 0.001)
	_, hasSize := sub["size"]
	assert.False(t, hasSize, "subscription has nil sizeFn; should not include size field")

	meta := byCache["roommeta"]
	require.NotNil(t, meta)
	assert.EqualValues(t, 1, meta["hits"])
	assert.EqualValues(t, 0, meta["misses"])
	assert.EqualValues(t, 1, meta["hit_rate"])
	assert.EqualValues(t, 7, meta["size"])
}

func TestStartLogger_NonPositiveIntervalIsNoop(t *testing.T) {
	s, _ := newStats(t)
	_ = s.Register("subscription", nil)

	cap, logger := newCaptureHandler()
	stop := s.StartLogger(context.Background(), 0, logger)
	defer stop()

	time.Sleep(20 * time.Millisecond)
	assert.Empty(t, cap.snapshot())
}

func TestStartLogger_StopFunctionCancelsGoroutine(t *testing.T) {
	s, _ := newStats(t)
	_ = s.Register("subscription", nil)

	cap, logger := newCaptureHandler()
	stop := s.StartLogger(context.Background(), 5*time.Millisecond, logger)

	// Let it tick at least once.
	time.Sleep(30 * time.Millisecond)
	stop() // synchronous: blocks until goroutine exits

	// Record count after cancellation.
	before := len(cap.snapshot())

	// Wait longer than the tick interval; count must not grow.
	time.Sleep(50 * time.Millisecond)
	after := len(cap.snapshot())
	assert.Equal(t, before, after, "stop should halt further log lines")
}

func TestStartLogger_NilLoggerUsesSlogDefault(t *testing.T) {
	// Smoke test: nil logger must not panic and must start the goroutine.
	s, _ := newStats(t)
	_ = s.Register("subscription", nil)

	stop := s.StartLogger(context.Background(), 1*time.Hour, nil)
	require.NotNil(t, stop)
	stop()
}

func TestStartLogger_ZeroDenominatorYieldsZeroRate(t *testing.T) {
	s, _ := newStats(t)
	_ = s.Register("subscription", nil)

	cap, logger := newCaptureHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := s.StartLogger(ctx, 5*time.Millisecond, logger)
	defer stop()

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && len(cap.snapshot()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	stop()

	lines := cap.snapshot()
	require.NotEmpty(t, lines)
	last := lines[len(lines)-1]
	assert.EqualValues(t, 0, last["hit_rate"], "rate should be 0 when no traffic")
}
