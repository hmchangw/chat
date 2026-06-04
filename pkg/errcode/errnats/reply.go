// Package errnats adapts errcode.Error to NATS request/reply responses.
package errnats

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/errcode"
)

const fallback = `{"code":"internal","error":"internal error"}`

// Marshal classifies err (logging it once) and returns the JSON envelope.
func Marshal(ctx context.Context, err error) []byte {
	data, mErr := json.Marshal(errcode.Classify(ctx, err))
	if mErr != nil {
		return []byte(fallback)
	}
	return data
}

// MarshalQuiet returns the envelope WITHOUT logging. Use only on paths that
// already logged the failure (panic backstop, admission/replyBusy).
func MarshalQuiet(err error) []byte {
	var e *errcode.Error
	if !errors.As(err, &e) {
		e = errcode.Internal("internal error")
	}
	data, mErr := json.Marshal(e)
	if mErr != nil {
		return []byte(fallback)
	}
	return data
}

// Reply classifies err (logging once) and sends the envelope on msg's reply subject.
func Reply(ctx context.Context, msg *nats.Msg, err error) {
	if rErr := msg.Respond(Marshal(ctx, err)); rErr != nil {
		slog.ErrorContext(ctx, "error reply failed", "error", rErr, "subject", msg.Subject)
	}
}

// ReplyQuiet sends the envelope WITHOUT logging the failure (see MarshalQuiet).
func ReplyQuiet(msg *nats.Msg, err error) {
	if rErr := msg.Respond(MarshalQuiet(err)); rErr != nil {
		slog.Error("error reply failed", "error", rErr, "subject", msg.Subject)
	}
}
