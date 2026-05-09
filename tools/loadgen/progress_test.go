package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// safeBuffer is a goroutine-safe wrapper over bytes.Buffer used by the
// progress tests, which read the captured slog output from one goroutine
// while the runProgress goroutine writes to it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, b.buf.Len())
	copy(out, b.buf.Bytes())
	return out
}

func (b *safeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *safeBuffer) String() string {
	return string(b.Bytes())
}

// captureSlog returns a goroutine-safe buffer-backed JSON slog logger.
func captureSlog(t *testing.T) (*slog.Logger, *safeBuffer) {
	t.Helper()
	buf := &safeBuffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// waitForLines polls buf until at least n newline-delimited lines exist
// or the deadline expires. Used to synchronize tests against the
// progress goroutine without introducing real-clock races.
func waitForLines(t *testing.T, buf *safeBuffer, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw := buf.Bytes()
		got := bytes.Count(bytes.TrimSpace(raw), []byte("\n"))
		if len(raw) > 0 && got+1 >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d progress lines; got %s", n, buf.String())
}

func TestProgress_EmitsOnTickerSignal(t *testing.T) {
	m := NewMetrics()
	logger, buf := captureSlog(t)

	tickCh := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runProgress(ctx, &progressConfig{
			Metrics: m,
			Preset:  "test",
			Logger:  logger,
			Ticks:   tickCh,
		})
		close(done)
	}()

	for i := 1; i <= 3; i++ {
		m.Published.WithLabelValues("test", "measured", "0", "100-200").Inc()
		tickCh <- time.Now()
		waitForLines(t, buf, i)
	}

	cancel()
	<-done

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.Len(t, lines, 3, "expected 3 progress lines, got %d:\n%s", len(lines), buf.String())
	for _, line := range lines {
		var obj map[string]any
		require.NoError(t, json.Unmarshal(line, &obj))
		assert.Equal(t, "progress", obj["msg"])
	}
}

func TestProgress_ComputesInstantaneousRate(t *testing.T) {
	m := NewMetrics()
	logger, buf := captureSlog(t)

	tickCh := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runProgress(ctx, &progressConfig{
			Metrics: m,
			Preset:  "test",
			Logger:  logger,
			Ticks:   tickCh,
		})
		close(done)
	}()

	for i := 0; i < 100; i++ {
		m.Published.WithLabelValues("test", "measured", "0", "100-200").Inc()
	}
	tickCh <- time.Now()
	waitForLines(t, buf, 1)

	for i := 0; i < 50; i++ {
		m.Published.WithLabelValues("test", "measured", "0", "100-200").Inc()
	}
	tickCh <- time.Now()
	waitForLines(t, buf, 2)

	cancel()
	<-done

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.GreaterOrEqual(t, len(lines), 2)
	var last map[string]any
	require.NoError(t, json.Unmarshal(lines[len(lines)-1], &last))
	deltaRaw, ok := last["delta_published"]
	require.True(t, ok, "progress line missing delta_published; got %s", string(lines[len(lines)-1]))
	assert.Equal(t, float64(50), deltaRaw)
}

func TestProgress_StopsOnContextCancel(t *testing.T) {
	m := NewMetrics()
	logger, buf := captureSlog(t)

	tickCh := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())

	var lineCount atomic.Int64
	done := make(chan struct{})
	go func() {
		runProgress(ctx, &progressConfig{
			Metrics: m, Preset: "test", Logger: logger, Ticks: tickCh,
		})
		close(done)
	}()

	tickCh <- time.Now()
	waitForLines(t, buf, 1)
	// Cancel BEFORE the next tick.
	cancel()
	// Allow the goroutine to exit.
	<-done

	// Try to send a tick now — should be ignored.
	select {
	case tickCh <- time.Now():
		t.Fatal("tick channel should be unconsumed after cancel")
	default:
	}

	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) > 0 && strings.Contains(string(line), `"msg":"progress"`) {
			lineCount.Add(1)
		}
	}
	assert.Equal(t, int64(1), lineCount.Load(),
		"only the first tick before cancel should have emitted; got %s", buf.String())
}
