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
	"github.com/stretchr/testify/assert"
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
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "not_found", got["code"])
	assert.Equal(t, "not_room_member", got["reason"])
	assert.Equal(t, "room not found", got["error"])
}

func TestMarshal_UnknownCollapsesToInternal(t *testing.T) {
	data := Marshal(ctxQuiet(), errors.New("mongo down"))
	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "internal", got["code"])
	assert.Equal(t, "internal error", got["error"])
	assert.NotContains(t, got, "reason", "reason should be absent")
}

func TestMarshalQuiet_DoesNotLogButStillCollapses(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)

	data := MarshalQuiet(errors.New("mongo down"))
	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "internal", got["code"])
	assert.Equal(t, "internal error", got["error"])
	assert.Empty(t, buf.String(), "MarshalQuiet must not log")
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
	assert.Equal(t, "forbidden", got["code"])
	assert.Equal(t, "not_room_member", got["reason"])
	assert.Equal(t, "not allowed", got["error"])

	// Exactly one Classify log line.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 1, "want exactly one log line, got: %s", buf.String())
	var line map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &line))
	assert.Equal(t, "request failed", line["msg"])
}

func TestReply_LogsAtErrorLevelOnInternal(t *testing.T) {
	ctx, buf := captureCtx()
	nc := startTestNATS(t)

	_ = requestAndCaptureReply(t, nc, "test.reply.internal", func(m *nats.Msg) {
		Reply(ctx, m, errcode.Internal("boom"))
	})

	var line map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &line))
	assert.Equal(t, "ERROR", line["level"], "internal must log at ERROR")
}

func TestReply_UnknownErrorCollapsesToInternal(t *testing.T) {
	ctx, buf := captureCtx()
	nc := startTestNATS(t)

	data := requestAndCaptureReply(t, nc, "test.reply.unknown", func(m *nats.Msg) {
		Reply(ctx, m, errors.New("mongo down 10.0.0.5"))
	})

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "internal", got["code"])
	assert.Equal(t, "internal error", got["error"])
	assert.NotContains(t, string(data), "mongo", "raw cause must NOT appear on the wire")
	assert.Contains(t, buf.String(), "mongo down", "raw cause must appear in the SERVER log")
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
	assert.Equal(t, "unavailable", got["code"])
	assert.Equal(t, "service busy", got["error"])
	assert.NotContains(t, buf.String(), "request failed", "ReplyQuiet must not emit a Classify log line")
}
