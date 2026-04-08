package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

const maxContentBytes = 20 * 1024 // 20 KB

// infraError represents a transient failure that should be nack'd for retry.
type infraError struct {
	cause error
}

func (e *infraError) Error() string {
	return e.cause.Error()
}

func (e *infraError) Unwrap() error {
	return e.cause
}

// replyFunc is the function signature for publishing a reply to a NATS subject.
type replyFunc func(ctx context.Context, subject string, data []byte) error

// publishFunc is the function signature for publishing to JetStream.
type publishFunc func(ctx context.Context, subject string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)

// Handler processes messages from the MESSAGES stream and validates them
// before publishing to MESSAGES_CANONICAL.
type Handler struct {
	store   Store
	publish publishFunc
	reply   replyFunc
	siteID  string
}

// NewHandler constructs a new Handler with the given dependencies.
func NewHandler(store Store, publish publishFunc, reply replyFunc, siteID string) *Handler {
	return &Handler{store: store, publish: publish, reply: reply, siteID: siteID}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES stream.
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	account, roomID, siteID, ok := subject.ParseUserRoomSiteSubject(msg.Subject())
	if !ok {
		slog.Warn("invalid subject", "subject", msg.Subject())
		if err := msg.Ack(); err != nil {
			slog.Error("failed to ack message", "error", err)
		}
		return
	}

	replyData, err := h.processMessage(ctx, account, roomID, siteID, msg.Data())
	if err != nil {
		slog.Error("process message failed", "error", err, "account", account, "roomID", roomID)
		var ie *infraError
		if errors.As(err, &ie) {
			if err := msg.Nak(); err != nil {
				slog.Error("failed to nack message", "error", err)
			}
		} else {
			// Validation error: reply with error and ack.
			h.sendReply(ctx, account, msg.Data(), natsutil.MarshalError(err.Error()))
			if err := msg.Ack(); err != nil {
				slog.Error("failed to ack message", "error", err)
			}
		}
		return
	}

	h.sendReply(ctx, account, msg.Data(), replyData)

	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "err", err)
	}
}

// sendReply extracts the requestID from the raw message data and publishes the
// reply payload to the user's response subject.
func (h *Handler) sendReply(ctx context.Context, account string, rawData []byte, replyData []byte) {
	var req model.SendMessageRequest
	if err := json.Unmarshal(rawData, &req); err != nil {
		slog.Error("unmarshal request for reply", "error", err)
		return
	}
	if req.RequestID == "" {
		return
	}
	respSubj := subject.UserResponse(account, req.RequestID)
	if err := h.reply(ctx, respSubj, replyData); err != nil {
		slog.Error("reply to client failed", "error", err, "subject", respSubj)
	}
}

// processMessage validates a SendMessageRequest and publishes a MessageEvent to MESSAGES_CANONICAL.
// Returns the serialized Message on success, or an error.
// Validation errors (bad input) are plain errors; transient failures are *infraError.
func (h *Handler) processMessage(ctx context.Context, account, roomID, siteID string, data []byte) ([]byte, error) {
	// Validate siteID matches this service's siteID
	if siteID != h.siteID {
		return nil, fmt.Errorf("siteID mismatch: got %s, want %s", siteID, h.siteID)
	}

	// Unmarshal request
	var req model.SendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("unmarshal send message request: %w", err)
	}

	// Validate ID is a valid UUID
	if _, err := uuid.Parse(req.ID); err != nil {
		return nil, fmt.Errorf("invalid message ID %q: %w", req.ID, err)
	}

	// Validate content is non-empty
	if req.Content == "" {
		return nil, fmt.Errorf("content must not be empty")
	}

	// Validate content does not exceed 20KB
	if len(req.Content) > maxContentBytes {
		return nil, fmt.Errorf("content exceeds maximum size of %d bytes", maxContentBytes)
	}

	// Verify subscription
	sub, err := h.store.GetSubscription(ctx, account, roomID)
	if err != nil {
		if errors.Is(err, errNotSubscribed) {
			return nil, fmt.Errorf("user %s is not subscribed to room %s", account, roomID)
		}
		return nil, &infraError{cause: fmt.Errorf("get subscription for user %s in room %s: %w", account, roomID, err)}
	}

	// Build Message
	now := time.Now().UTC()
	msg := model.Message{
		ID:                    req.ID,
		RoomID:                roomID,
		UserID:                sub.User.ID,
		UserAccount:           sub.User.Account,
		Content:               req.Content,
		CreatedAt:             now,
		ThreadParentMessageID: req.ThreadParentMessageID,
	}

	// Publish MessageEvent to MESSAGES_CANONICAL
	evt := model.MessageEvent{Message: msg, SiteID: siteID}
	evtData, err := json.Marshal(evt)
	if err != nil {
		return nil, &infraError{cause: fmt.Errorf("marshal message event: %w", err)}
	}

	canonicalSubj := subject.MsgCanonicalCreated(siteID)
	if _, err := h.publish(ctx, canonicalSubj, evtData, jetstream.WithMsgID(msg.ID)); err != nil {
		return nil, &infraError{cause: fmt.Errorf("publish to MESSAGES_CANONICAL: %w", err)}
	}

	return json.Marshal(msg)
}
