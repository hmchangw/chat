package errcode

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestWithLogValues_AccumulatesAttrs(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))
	ctx = WithLogValues(ctx, "account", "alice")
	ctx = WithLogValues(ctx, "roomID", "r1")
	loggerFrom(ctx).Info("hello")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["account"] != "alice" || line["roomID"] != "r1" {
		t.Fatalf("attrs not accumulated: %v", line)
	}
}

func TestLoggerFrom_DefaultsWhenAbsent(t *testing.T) {
	if loggerFrom(context.Background()) == nil {
		t.Fatal("loggerFrom must never return nil")
	}
}
