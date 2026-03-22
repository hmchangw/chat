package natsutil

import (
	"encoding/json"
	"log/slog"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/nats-io/nats.go"
)

func MarshalResponse(v any) ([]byte, error) {
	return json.Marshal(v)
}

func MarshalError(errMsg string) []byte {
	data, _ := json.Marshal(model.ErrorResponse{Error: errMsg})
	return data
}

func ReplyJSON(msg *nats.Msg, v any) {
	data, err := MarshalResponse(v)
	if err != nil {
		ReplyError(msg, "marshal error: "+err.Error())
		return
	}
	if err := msg.Respond(data); err != nil {
		slog.Error("reply failed", "error", err)
	}
}

func ReplyError(msg *nats.Msg, errMsg string) {
	if err := msg.Respond(MarshalError(errMsg)); err != nil {
		slog.Error("error reply failed", "error", err)
	}
}
