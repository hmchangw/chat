// Package natsutil holds the few JSON/reply helpers the chat services share
// for NATS request/reply. Client-facing errors flow through pkg/errcode
// (errnats.Reply / errhttp.Write) — this package is success-reply mechanics
// only; the legacy MarshalError/MarshalErrorWithCode/ReplyError/TryParseError
// helpers were deleted alongside model.ErrorResponse.
package natsutil

import (
	"encoding/json"
	"log/slog"

	"github.com/nats-io/nats.go"
)

// MarshalResponse encodes a value as JSON for NATS responses.
func MarshalResponse(v any) ([]byte, error) {
	return json.Marshal(v)
}

// ReplyJSON sends a JSON-encoded success response on msg's reply subject.
// On a marshal failure (an unmarshalable v — typically a programmer error),
// responds with a generic internal-error errcode envelope so the caller is
// not left hanging.
func ReplyJSON(msg *nats.Msg, v any) {
	data, err := MarshalResponse(v)
	if err != nil {
		slog.Error("marshal response failed", "error", err, "subject", msg.Subject)
		if rErr := msg.Respond([]byte(`{"code":"internal","error":"internal error"}`)); rErr != nil {
			slog.Error("reply failed", "error", rErr, "subject", msg.Subject)
		}
		return
	}
	if err := msg.Respond(data); err != nil {
		slog.Error("reply failed", "error", err, "subject", msg.Subject)
	}
}
