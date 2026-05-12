package integrationsuite

import (
	"encoding/json"
	"errors"

	"github.com/nats-io/nats.go"
)

// ParseNATSReplyError returns the "error" string from a reply body
// that follows the model.ErrorResponse shape ({"error":"..."}).
// Returns "" if the body is not an error response or the field is empty.
func ParseNATSReplyError(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var er struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &er); err != nil {
		return ""
	}
	return er.Error
}

// MapNATSTransportError maps a NATS-level error (returned by
// nc.Request, nc.RequestMsg, etc.) to a Class.
// Returns ClassNone if err is nil.
func MapNATSTransportError(err error) Class {
	if err == nil {
		return ClassNone
	}
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return ClassRouteNotFound
	case errors.Is(err, nats.ErrTimeout):
		return ClassTimeout
	case errors.Is(err, nats.ErrConnectionClosed),
		errors.Is(err, nats.ErrConnectionDraining),
		errors.Is(err, nats.ErrConnectionReconnecting):
		return ClassUnreachable
	default:
		return ClassUnclassified
	}
}
