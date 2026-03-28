package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
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

// publishFunc is the function signature for publishing to JetStream.
type publishFunc func(subject string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)

// Handler processes messages from the MESSAGES stream and validates them
// before publishing to MESSAGE_SSOT.
type Handler struct {
	store   Store
	publish publishFunc
	siteID  string
}

// NewHandler constructs a new Handler with the given dependencies.
func NewHandler(store Store, publish publishFunc, siteID string) *Handler {
	return &Handler{store: store, publish: publish, siteID: siteID}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES stream.
func (h *Handler) HandleJetStreamMsg(msg jetstream.Msg) {
	username, roomID, siteID, ok := subject.ParseUserRoomSiteSubject(msg.Subject())
	if !ok {
		slog.Warn("invalid subject", "subject", msg.Subject())
		if err := msg.Ack(); err != nil {
			slog.Error("failed to ack message", "error", err)
		}
		return
	}

	ctx := context.Background()
	_, err := h.processMessage(ctx, username, roomID, siteID, msg.Data())
	if err != nil {
		slog.Error("process message failed", "error", err, "username", username, "roomID", roomID)
		var ie *infraError
		if isInfraError(err, &ie) {
			if err := msg.Nak(); err != nil {
				slog.Error("failed to nack message", "error", err)
			}
		} else {
			if err := msg.Ack(); err != nil {
				slog.Error("failed to ack message", "error", err)
			}
		}
		return
	}

	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "err", err)
	}
}

func isInfraError(err error, ie **infraError) bool {
	var e *infraError
	if ok := asInfraError(err, &e); ok {
		*ie = e
		return true
	}
	return false
}

func asInfraError(err error, target **infraError) bool {
	type unwrapper interface {
		Unwrap() error
	}
	for err != nil {
		if ie, ok := err.(*infraError); ok {
			*target = ie
			return true
		}
		if uw, ok := err.(unwrapper); ok {
			err = uw.Unwrap()
		} else {
			break
		}
	}
	return false
}

// processMessage validates a SendMessageRequest and publishes a MessageEvent to MESSAGE_SSOT.
// Returns the serialized Message on success, or an error.
// Validation errors (bad input) are plain errors; transient failures are *infraError.
func (h *Handler) processMessage(ctx context.Context, username, roomID, siteID string, data []byte) ([]byte, error) {
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
	sub, err := h.store.GetSubscription(ctx, username, roomID)
	if err != nil {
		return nil, &infraError{cause: fmt.Errorf("get subscription for user %s in room %s: %w", username, roomID, err)}
	}

	// Build Message
	now := time.Now().UTC()
	msg := model.Message{
		ID:        req.ID,
		RoomID:    roomID,
		UserID:    sub.User.ID,
		Username:  sub.User.Username,
		Content:   req.Content,
		CreatedAt: now,
	}

	// Publish MessageEvent to MESSAGE_SSOT
	evt := model.MessageEvent{Message: msg, SiteID: siteID}
	evtData, err := json.Marshal(evt)
	if err != nil {
		return nil, &infraError{cause: fmt.Errorf("marshal message event: %w", err)}
	}

	ssotSubj := subject.MsgSSOTCreated(siteID)
	if _, err := h.publish(ssotSubj, evtData, jetstream.WithMsgID(msg.ID)); err != nil {
		return nil, &infraError{cause: fmt.Errorf("publish to MESSAGE_SSOT: %w", err)}
	}

	return json.Marshal(msg)
}
