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

	"github.com/hmchangw/chat/pkg/mention"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/userstore"
)

type Handler struct {
	store       Store
	userStore   userstore.UserStore
	threadStore ThreadStore
}

func NewHandler(store Store, userStore userstore.UserStore, threadStore ThreadStore) *Handler {
	return &Handler{store: store, userStore: userStore, threadStore: threadStore}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES_CANONICAL stream.
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	if err := h.processMessage(ctx, msg.Data()); err != nil {
		slog.Error("process message failed", "error", err)
		if err := msg.Nak(); err != nil {
			slog.Error("failed to nack message", "error", err)
		}
		return
	}

	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "err", err)
	}
}

func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	resolved, err := mention.Resolve(ctx, evt.Message.Content, h.userStore.FindUsersByAccounts)
	if err != nil {
		return fmt.Errorf("resolve mentions: %w", err)
	}
	evt.Message.Mentions = resolved.Participants

	user, err := h.userStore.FindUserByID(ctx, evt.Message.UserID)
	if err != nil {
		return fmt.Errorf("lookup user %s: %w", evt.Message.UserID, err)
	}

	sender := cassParticipant{
		ID:          user.ID,
		EngName:     user.EngName,
		CompanyName: user.ChineseName,
		Account:     evt.Message.UserAccount,
	}

	if evt.Message.ThreadParentMessageID != "" {
		// Resolve (or create) the thread room first so we have the threadRoomID
		// before persisting the message to Cassandra.
		threadRoomID, err := h.handleThreadRoomAndSubscriptions(ctx, &evt.Message, evt.SiteID)
		if err != nil {
			return fmt.Errorf("handle thread room and subscriptions: %w", err)
		}
		if err := h.store.SaveThreadMessage(ctx, &evt.Message, &sender, evt.SiteID, threadRoomID); err != nil {
			return fmt.Errorf("save thread message: %w", err)
		}
	} else {
		if err := h.store.SaveMessage(ctx, &evt.Message, &sender, evt.SiteID); err != nil {
			return fmt.Errorf("save message: %w", err)
		}
	}

	return nil
}

// handleThreadRoomAndSubscriptions creates the ThreadRoom on first reply, and
// upserts ThreadSubscriptions for the parent author and the replier. On subsequent
// replies it updates the existing ThreadRoom's last message and ensures both the
// parent author and the replier have subscriptions. All operations are idempotent.
// It returns the threadRoomID so the caller can pass it to SaveThreadMessage.
func (h *Handler) handleThreadRoomAndSubscriptions(ctx context.Context, msg *model.Message, siteID string) (string, error) {
	now := msg.CreatedAt

	threadRoom := model.ThreadRoom{
		ID:              uuid.NewString(),
		ParentMessageID: msg.ThreadParentMessageID,
		RoomID:          msg.RoomID,
		SiteID:          siteID,
		LastMsgAt:       msg.CreatedAt,
		LastMsgID:       msg.ID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	err := h.threadStore.CreateThreadRoom(ctx, &threadRoom)
	switch {
	case err == nil:
		return threadRoom.ID, h.handleFirstThreadReply(ctx, msg, siteID, threadRoom.ID, now)
	case errors.Is(err, errThreadRoomExists):
		return h.handleSubsequentThreadReply(ctx, msg, siteID, now)
	default:
		return "", fmt.Errorf("create thread room: %w", err)
	}
}

// handleFirstThreadReply runs after the thread room has just been created.
// It upserts subscriptions for the parent author (unseen reply) and, if distinct,
// for the replier (already seen — they just posted).
func (h *Handler) handleFirstThreadReply(ctx context.Context, msg *model.Message, siteID, threadRoomID string, now time.Time) error {
	parentSender, err := h.store.GetMessageSender(ctx, msg.ThreadParentMessageID)
	if err != nil {
		if errors.Is(err, errMessageNotFound) {
			slog.Warn("thread reply parent not found — skipping subscription creation",
				"parentMessageID", msg.ThreadParentMessageID,
				"replyID", msg.ID)
			return nil
		}
		return fmt.Errorf("get parent message sender: %w", err)
	}

	if err := h.threadStore.UpsertThreadSubscription(ctx,
		h.buildThreadSubscription(msg, threadRoomID, parentSender.ID, parentSender.Account, siteID, time.Time{}, now),
	); err != nil {
		return fmt.Errorf("upsert parent author thread subscription: %w", err)
	}

	if msg.UserID != parentSender.ID {
		if err := h.threadStore.UpsertThreadSubscription(ctx,
			h.buildThreadSubscription(msg, threadRoomID, msg.UserID, msg.UserAccount, siteID, now, now),
		); err != nil {
			return fmt.Errorf("upsert replier thread subscription: %w", err)
		}
	}

	return nil
}

// handleSubsequentThreadReply runs when CreateThreadRoom reported an existing
// room. It fetches the existing room, ensures the parent author is subscribed
// (guards against an orphaned subscription from a partial first-reply failure),
// upserts the replier, and bumps the room's last-message pointer.
// Returns the existing thread room ID so the caller can pass it to SaveThreadMessage.
func (h *Handler) handleSubsequentThreadReply(ctx context.Context, msg *model.Message, siteID string, now time.Time) (string, error) {
	existingRoom, err := h.threadStore.GetThreadRoomByParentMessageID(ctx, msg.ThreadParentMessageID)
	if err != nil {
		return "", fmt.Errorf("get existing thread room: %w", err)
	}

	parentSender, err := h.store.GetMessageSender(ctx, msg.ThreadParentMessageID)
	if err != nil {
		if errors.Is(err, errMessageNotFound) {
			slog.Warn("thread reply parent not found — skipping subscription upsert",
				"parentMessageID", msg.ThreadParentMessageID,
				"replyID", msg.ID)
			return existingRoom.ID, nil
		}
		return "", fmt.Errorf("get parent message sender: %w", err)
	}

	if err := h.threadStore.UpsertThreadSubscription(ctx,
		h.buildThreadSubscription(msg, existingRoom.ID, parentSender.ID, parentSender.Account, siteID, time.Time{}, now),
	); err != nil {
		return "", fmt.Errorf("upsert parent author thread subscription: %w", err)
	}

	if msg.UserID != parentSender.ID {
		if err := h.threadStore.UpsertThreadSubscription(ctx,
			h.buildThreadSubscription(msg, existingRoom.ID, msg.UserID, msg.UserAccount, siteID, now, now),
		); err != nil {
			return "", fmt.Errorf("upsert replier thread subscription: %w", err)
		}
	}

	if err := h.threadStore.UpdateThreadRoomLastMessage(ctx, existingRoom.ID, msg.ID, msg.CreatedAt); err != nil {
		return "", fmt.Errorf("update thread room last message: %w", err)
	}

	return existingRoom.ID, nil
}

// buildThreadSubscription constructs a ThreadSubscription for (threadRoomID, userID).
// lastSeenAt is separate from now because the replier has "seen" their own reply
// (lastSeenAt = now) while the parent author has not (lastSeenAt = time.Time{}).
func (h *Handler) buildThreadSubscription(msg *model.Message, threadRoomID, userID, userAccount, siteID string, lastSeenAt, now time.Time) *model.ThreadSubscription {
	return &model.ThreadSubscription{
		ID:              uuid.NewString(),
		ParentMessageID: msg.ThreadParentMessageID,
		RoomID:          msg.RoomID,
		ThreadRoomID:    threadRoomID,
		UserID:          userID,
		UserAccount:     userAccount,
		SiteID:          siteID,
		LastSeenAt:      lastSeenAt,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}
