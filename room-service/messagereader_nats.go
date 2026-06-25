package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// natsMessageReader implements MessageReader by issuing a GetMessageByID request
// to history-service over NATS, replacing a direct Cassandra read so room-service
// holds no gocql session. siteID addresses the request at the local history-service.
type natsMessageReader struct {
	nc      *nats.Conn
	siteID  string
	timeout time.Duration
}

// newNATSMessageReader returns a NATS-backed MessageReader. Returns the concrete
// type ("accept interfaces, return structs"); main.go injects it as a MessageReader.
func newNATSMessageReader(nc *nats.Conn, siteID string, timeout time.Duration) *natsMessageReader {
	return &natsMessageReader{nc: nc, siteID: siteID, timeout: timeout}
}

// getMessageByIDRequest mirrors history-service's GetMessageByIDRequest wire shape
// (the source struct lives under internal/ and isn't importable).
type getMessageByIDRequest struct {
	MessageID string `json:"messageId"`
}

// messageProjection decodes only the fields the read-receipt path needs from the
// GetMessageByID reply, avoiding the full cassandra.Message (whose struct-keyed
// Reactions map carries a marshal-only decoder).
type messageProjection struct {
	RoomID    string                `json:"roomId"`
	CreatedAt time.Time             `json:"createdAt"`
	Sender    cassandra.Participant `json:"sender"`
}

// GetMessageRoomAndCreatedAt resolves a message's room, createdAt, and sender via
// history-service's GetMessageByID at subject.MsgGet(account, roomID, siteID).
// history-service scopes the lookup to roomID and enforces the requester's access
// window, so a not_found or forbidden reply means "no usable row" → found=false
// (the handler then returns its message-not-found / not-a-member errors). Only
// infra failures (NATS timeout/no-responder, other errcodes, decode errors)
// surface as an error for the caller to propagate.
func (r *natsMessageReader) GetMessageRoomAndCreatedAt(
	ctx context.Context, account, roomID, messageID string,
) (string, time.Time, string, bool, error) {
	body, err := json.Marshal(getMessageByIDRequest{MessageID: messageID})
	if err != nil {
		return "", time.Time{}, "", false, fmt.Errorf("marshal GetMessageByID request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	out := natsutil.NewMsg(reqCtx, subject.MsgGet(account, roomID, r.siteID), body)
	reply, err := r.nc.RequestMsgWithContext(reqCtx, out)
	if err != nil {
		return "", time.Time{}, "", false, fmt.Errorf("history GetMessageByID request: %w", err)
	}

	if ee, ok := errcode.Parse(reply.Data); ok && ee.Code.Valid() {
		// not_found (missing / different room) and forbidden (outside access
		// window) both mean the requester has no usable message → found=false.
		if ee.Code == errcode.CodeNotFound || ee.Code == errcode.CodeForbidden {
			return "", time.Time{}, "", false, nil
		}
		return "", time.Time{}, "", false, fmt.Errorf("history GetMessageByID: %w", ee)
	}

	var msg messageProjection
	if err := json.Unmarshal(reply.Data, &msg); err != nil {
		return "", time.Time{}, "", false, fmt.Errorf("unmarshal message reply: %w", err)
	}
	return msg.RoomID, msg.CreatedAt, msg.Sender.Account, true, nil
}
