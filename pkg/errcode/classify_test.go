package errcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func newCapture() (context.Context, *bytes.Buffer) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return WithLogger(context.Background(), l), &buf
}

func TestClassify_NilReturnsNil(t *testing.T) {
	ctx, _ := newCapture()
	if Classify(ctx, nil) != nil {
		t.Fatal("nil → nil")
	}
}

func TestClassify_UnknownBecomesInternalAndLogsCause(t *testing.T) {
	ctx, buf := newCapture()
	raw := fmt.Errorf("load room: %w", errors.New("mongo: connection refused 10.0.0.5"))
	e := Classify(ctx, raw)
	if e.Code != CodeInternal || e.Message != "internal error" {
		t.Fatalf("got %+v", e)
	}
	if !strings.Contains(buf.String(), "mongo: connection refused") {
		t.Fatalf("cause not logged: %s", buf.String())
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "mongo") {
		t.Fatalf("cause leaked into reply: %s", b)
	}
}

func TestClassify_TypedErrorPreservedThroughWrapping(t *testing.T) {
	ctx, _ := newCapture()
	typed := NotFound("room not found", WithReason("room_not_found"))
	e := Classify(ctx, fmt.Errorf("checking room: %w", typed))
	if e.Code != CodeNotFound || e.Reason != "room_not_found" {
		t.Fatalf("typed lost: %+v", e)
	}
}

func TestClassify_LogsCtxValues(t *testing.T) {
	ctx, buf := newCapture()
	ctx = WithLogValues(ctx, "request_id", "req-123", "account", "alice")
	Classify(ctx, errors.New("boom"))
	if l := buf.String(); !strings.Contains(l, "req-123") || !strings.Contains(l, "alice") {
		t.Fatalf("ctx values missing: %s", l)
	}
}

func TestClassify_LogsAttachedCause(t *testing.T) {
	// The whole point of WithCause: the raw underlying error must appear in the
	// server log, even though the client only sees "internal error".
	raw := errors.New("mongo: connection refused 10.0.0.5")

	// Direct: errcode error with a cause.
	ctx, buf := newCapture()
	e := Classify(ctx, Internal("internal error", WithCause(raw)))
	if !strings.Contains(buf.String(), "mongo: connection refused") {
		t.Fatalf("direct WithCause not logged: %s", buf.String())
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "mongo") {
		t.Fatalf("cause leaked into reply: %s", b)
	}

	// Wrapped: outer context + the hidden cause must both survive.
	ctx, buf = newCapture()
	Classify(ctx, fmt.Errorf("handler save: %w", Internal("internal error", WithCause(raw))))
	if l := buf.String(); !strings.Contains(l, "handler save") || !strings.Contains(l, "mongo: connection refused") {
		t.Fatalf("wrapped cause/context lost: %s", l)
	}
}

func TestClassify_CauseAndUnderlyingAreSeparateLogFields(t *testing.T) {
	// Task 20.20: the underlying-cause text is logged as its own slog field so
	// log aggregators can pivot on it independently and Classify avoids the
	// per-request string-concat allocation.
	ctx, buf := newCapture()
	raw := errors.New("mongo: connection refused 10.0.0.5")
	Classify(ctx, fmt.Errorf("handler save: %w", Internal("internal error", WithCause(raw))))
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log not JSON: %v", err)
	}
	cause, _ := line["cause"].(string)
	underlying, _ := line["underlying"].(string)
	if !strings.Contains(cause, "handler save") {
		t.Fatalf(`cause field missing outer message: %q`, cause)
	}
	if !strings.Contains(underlying, "mongo: connection refused") {
		t.Fatalf(`underlying field missing raw cause: %q`, underlying)
	}
	if strings.Contains(cause, "mongo: connection refused") {
		t.Fatalf("raw cause must NOT be concatenated into cause field: %q", cause)
	}
}

func TestClassify_NoUnderlyingFieldWhenNoCause(t *testing.T) {
	ctx, buf := newCapture()
	Classify(ctx, NotFound("room not found"))
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log not JSON: %v", err)
	}
	if _, present := line["underlying"]; present {
		t.Fatalf("underlying field present without WithCause: %s", buf.String())
	}
}

func TestClassify_LevelIsCategoryAware(t *testing.T) {
	level := func(err error) string {
		ctx, buf := newCapture()
		Classify(ctx, err)
		var line map[string]any
		_ = json.Unmarshal(buf.Bytes(), &line)
		return line["level"].(string)
	}
	// Expected client errors must NOT log at ERROR (would pollute alerting).
	if got := level(BadRequest("name is required")); got != "INFO" {
		t.Fatalf("4xx level = %s, want INFO", got)
	}
	if got := level(NotFound("gone")); got != "INFO" {
		t.Fatalf("not_found level = %s, want INFO", got)
	}
	// Server/infra errors log at ERROR.
	if got := level(errors.New("mongo down")); got != "ERROR" {
		t.Fatalf("internal level = %s, want ERROR", got)
	}
	if got := level(Unavailable("service busy")); got != "ERROR" {
		t.Fatalf("unavailable level = %s, want ERROR", got)
	}
}
