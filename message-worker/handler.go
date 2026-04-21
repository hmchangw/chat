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
		if nakErr := msg.Nak(); nakErr != nil {
			slog.Error("failed to nack message", "error", nakErr)
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

	var sender *cassParticipant
	user, err := h.userStore.FindUserByID(ctx, evt.Message.UserID)
	if err != nil {
		if evt.Message.Type != "" {
			// System messages may have no real user; proceed with nil sender.
			slog.Warn("user not found for system message, using nil sender",
				"userID", evt.Message.UserID, "type", evt.Message.Type)
		} else {
			return fmt.Errorf("lookup user %s: %w", evt.Message.UserID, err)
		}
	} else {
		sender = &cassParticipant{
			ID:          user.ID,
			EngName:     user.EngName,
			CompanyName: user.ChineseName,
			Account:     evt.Message.UserAccount,
		}
	}

	if evt.Message.ThreadParentMessageID != "" {
		// Resolve (or create) the thread room first so we have the threadRoomID
		// before persisting the message to Cassandra.
		threadRoomID, err := h.handleThreadRoomAndSubscriptions(ctx, &evt.Message, evt.SiteID)
		if err != nil {
			return fmt.Errorf("handle thread room and subscriptions: %w", err)
		}
		if err := h.markThreadMentions(ctx, &evt.Message, threadRoomID, evt.SiteID); err != nil {
			return fmt.Errorf("mark thread mentions: %w", err)
		}
		if err := h.store.SaveThreadMessage(ctx, &evt.Message, sender, evt.SiteID, threadRoomID); err != nil {
			return fmt.Errorf("save thread message: %w", err)
		}
	} else {
		if err := h.store.SaveMessage(ctx, &evt.Message, sender, evt.SiteID); err != nil {
			return fmt.Errorf("save message: %w", err)
		}
	}

	return nil
}

// handleThreadRoomAndSubscriptions creates the ThreadRoom on first reply and
// inserts ThreadSubscriptions for the parent author and replier. On subsequent
// replies it checks whether the replier already has a subscription (inserting
// if not) and bumps the room's last-message pointer.
// It returns the threadRoomID so the caller can pass it to SaveThreadMessage.
func (h *Handler) handleThreadRoomAndSubscriptions(ctx context.Context, msg *model.Message, siteID string) (string, error) {
	now := msg.CreatedAt

	threadRoom := model.ThreadRoom{
		ID:              uuid.NewString(),
		ParentMessageID: msg.ThreadParentMessageID,
		RoomID:          msg.RoomID,
		SiteID:          siteID,
		LastMsgAt:       now,
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
// It inserts subscriptions for the parent author and, if distinct, for the replier.
// lastSeenAt is always nil — the subscription is brand-new and the user has not
// yet "seen" the thread.
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

	if err := h.threadStore.InsertThreadSubscription(ctx,
		h.buildThreadSubscription(msg, threadRoomID, parentSender.ID, parentSender.Account, siteID, now),
	); err != nil {
		return fmt.Errorf("insert parent author thread subscription: %w", err)
	}

	if msg.UserID != parentSender.ID {
		if err := h.threadStore.InsertThreadSubscription(ctx,
			h.buildThreadSubscription(msg, threadRoomID, msg.UserID, msg.UserAccount, siteID, now),
		); err != nil {
			return fmt.Errorf("insert replier thread subscription: %w", err)
		}
	}

	return nil
}

// handleSubsequentThreadReply runs when CreateThreadRoom reported an existing room.
// It upserts subscriptions for both the parent author and the replier — idempotent
// so redeliveries after a partial first attempt never lose the parent subscription —
// then bumps the room's last-message pointer.
// Returns the existing thread room ID so the caller can pass it to SaveThreadMessage.
func (h *Handler) handleSubsequentThreadReply(ctx context.Context, msg *model.Message, siteID string, now time.Time) (string, error) {
	existingRoom, err := h.threadStore.GetThreadRoomByParentMessageID(ctx, msg.ThreadParentMessageID)
	if err != nil {
		return "", fmt.Errorf("get existing thread room: %w", err)
	}

	parentSender, err := h.store.GetMessageSender(ctx, msg.ThreadParentMessageID)
	switch {
	case err == nil:
		if err := h.threadStore.UpsertThreadSubscription(ctx,
			h.buildThreadSubscription(msg, existingRoom.ID, parentSender.ID, parentSender.Account, siteID, now),
		); err != nil {
			return "", fmt.Errorf("upsert parent author thread subscription: %w", err)
		}
		if msg.UserID != parentSender.ID {
			if err := h.threadStore.UpsertThreadSubscription(ctx,
				h.buildThreadSubscription(msg, existingRoom.ID, msg.UserID, msg.UserAccount, siteID, now),
			); err != nil {
				return "", fmt.Errorf("upsert replier thread subscription: %w", err)
			}
		}
	case errors.Is(err, errMessageNotFound):
		slog.Warn("thread reply parent not found — skipping parent subscription upsert",
			"parentMessageID", msg.ThreadParentMessageID,
			"replyID", msg.ID)
		if err := h.threadStore.UpsertThreadSubscription(ctx,
			h.buildThreadSubscription(msg, existingRoom.ID, msg.UserID, msg.UserAccount, siteID, now),
		); err != nil {
			return "", fmt.Errorf("upsert replier thread subscription: %w", err)
		}
	default:
		return "", fmt.Errorf("get parent message sender: %w", err)
	}

	if err := h.threadStore.UpdateThreadRoomLastMessage(ctx, existingRoom.ID, msg.ID, now); err != nil {
		return "", fmt.Errorf("update thread room last message: %w", err)
	}

	return existingRoom.ID, nil
}

// buildThreadSubscription constructs a ThreadSubscription for (threadRoomID, userID).
// lastSeenAt is always nil — subscriptions are insert-only; the field is never
// updated by the message worker.
func (h *Handler) buildThreadSubscription(msg *model.Message, threadRoomID, userID, userAccount, siteID string, now time.Time) *model.ThreadSubscription {
	return &model.ThreadSubscription{
		ID:              uuid.NewString(),
		ParentMessageID: msg.ThreadParentMessageID,
		RoomID:          msg.RoomID,
		ThreadRoomID:    threadRoomID,
		UserID:          userID,
		UserAccount:     userAccount,
		SiteID:          siteID,
		LastSeenAt:      nil,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// markThreadMentions flips hasMention=true on the thread subscription of every
// @account mentionee in msg (auto-creating the subscription if absent). The
// sender is excluded, and @all is ignored at the thread level.
func (h *Handler) markThreadMentions(ctx context.Context, msg *model.Message, threadRoomID, siteID string) error {
	for i := range msg.Mentions {
		p := &msg.Mentions[i]
		if p.Account == "all" {
			continue
		}
		if p.UserID == msg.UserID {
			continue
		}
		sub := h.buildThreadSubscription(msg, threadRoomID, p.UserID, p.Account, siteID, msg.CreatedAt)
		sub.HasMention = true
		if err := h.threadStore.MarkThreadSubscriptionMention(ctx, sub); err != nil {
			return fmt.Errorf("mark thread subscription mention for user %s: %w", p.UserID, err)
		}
	}
	return nil
}
