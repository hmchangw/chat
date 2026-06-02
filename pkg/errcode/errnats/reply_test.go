package errnats

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/errcode"
)

// startTestNATS spins up an in-memory NATS server bound to a random port and
// returns a connected *nats.Conn. Cleanup runs on t.Cleanup.
func startTestNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natsserver.Options{Port: -1}
	ns, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second), "nats server did not become ready")
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// captureCtx returns a ctx whose logger writes JSON to buf.
func captureCtx() (context.Context, *bytes.Buffer) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return errcode.WithLogger(context.Background(), l), &buf
}

func ctxQuiet() context.Context {
	return errcode.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
}

func TestMarshal_TypedError(t *testing.T) {
	data := Marshal(ctxQuiet(), errcode.NotFound("room not found", errcode.WithReason(errcode.RoomNotMember)))
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if got["code"] != "not_found" || got["reason"] != "not_room_member" || got["error"] != "room not found" {
		t.Fatalf("envelope = %v", got)
	}
}

func TestMarshal_UnknownCollapsesToInternal(t *testing.T) {
	data := Marshal(ctxQuiet(), errors.New("mongo down"))
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if got["code"] != "internal" || got["error"] != "internal error" {
		t.Fatalf("envelope = %v", got)
	}
	if _, leaked := got["reason"]; leaked {
		t.Fatal("reason should be absent")
	}
}

func TestMarshalQuiet_DoesNotLogButStillCollapses(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)

	data := MarshalQuiet(errors.New("mongo down"))
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if got["code"] != "internal" || got["error"] != "internal error" {
		t.Fatalf("envelope = %v", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("MarshalQuiet must not log; got %s", buf.String())
	}
}

// requestAndCaptureReply opens a subscriber on subj that runs handler on each
// inbound msg, then publishes a request and returns the reply bytes.
func requestAndCaptureReply(t *testing.T, nc *nats.Conn, subj string, handler func(m *nats.Msg)) []byte {
	t.Helper()
	sub, err := nc.Subscribe(subj, handler)
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()
	reply, err := nc.Request(subj, []byte(`{}`), 2*time.Second)
	require.NoError(t, err)
	return reply.Data
}

func TestReply_RespondsWithEnvelopeAndLogsOnce(t *testing.T) {
	ctx, buf := captureCtx()
	nc := startTestNATS(t)

	data := requestAndCaptureReply(t, nc, "test.reply.fb", func(m *nats.Msg) {
		Reply(ctx, m, errcode.Forbidden("not allowed", errcode.WithReason(errcode.RoomNotMember)))
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	if got["code"] != "forbidden" || got["reason"] != "not_room_member" || got["error"] != "not allowed" {
		t.Fatalf("envelope = %v", got)
	}

	// Exactly one Classify log line.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 log line, got %d: %s", len(lines), buf.String())
	}
	var line map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &line))
	if line["msg"] != "request failed" {
		t.Fatalf("unexpected log line: %v", line)
	}
}

func TestReply_LogsAtErrorLevelOnInternal(t *testing.T) {
	ctx, buf := captureCtx()
	nc := startTestNATS(t)

	_ = requestAndCaptureReply(t, nc, "test.reply.internal", func(m *nats.Msg) {
		Reply(ctx, m, errcode.Internal("boom"))
	})

	var line map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &line))
	if line["level"] != "ERROR" {
		t.Fatalf("internal must log at ERROR, got level=%v", line["level"])
	}
}

func TestReply_UnknownErrorCollapsesToInternal(t *testing.T) {
	ctx, buf := captureCtx()
	nc := startTestNATS(t)

	data := requestAndCaptureReply(t, nc, "test.reply.unknown", func(m *nats.Msg) {
		Reply(ctx, m, errors.New("mongo down 10.0.0.5"))
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	if got["code"] != "internal" || got["error"] != "internal error" {
		t.Fatalf("wire envelope leaked: %v", got)
	}
	if strings.Contains(string(data), "mongo") {
		t.Fatal("raw cause must NOT appear on the wire")
	}
	if !strings.Contains(buf.String(), "mongo down") {
		t.Fatalf("raw cause must appear in the SERVER log: %s", buf.String())
	}
}

func TestReplyQuiet_RespondsButEmitsNoClassifyLine(t *testing.T) {
	nc := startTestNATS(t)

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)

	data := requestAndCaptureReply(t, nc, "test.reply.quiet", func(m *nats.Msg) {
		ReplyQuiet(m, errcode.Unavailable("service busy"))
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	if got["code"] != "unavailable" || got["error"] != "service busy" {
		t.Fatalf("envelope = %v", got)
	}
	if strings.Contains(buf.String(), "request failed") {
		t.Fatalf("ReplyQuiet must not emit a Classify log line; got %s", buf.String())
	}
}
