package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/mention"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/userstore"
)

// PublishFunc publishes data; non-empty msgID sets Nats-Msg-Id for JetStream stream-level dedup.
// Mirrors room-worker's PublishFunc signature so message-worker can plug into the same publish closure.
type PublishFunc func(ctx context.Context, subj string, data []byte, msgID string) error

type Handler struct {
	store       Store
	userStore   userstore.UserStore
	threadStore ThreadStore
	siteID      string
	publish     PublishFunc
}

func NewHandler(store Store, userStore userstore.UserStore, threadStore ThreadStore, siteID string, publish PublishFunc) *Handler {
	return &Handler{
		store:       store,
		userStore:   userStore,
		threadStore: threadStore,
		siteID:      siteID,
		publish:     publish,
	}
}

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
		threadRoomID, err := h.handleThreadRoomAndSubscriptions(ctx, &evt.Message, evt.SiteID, user)
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
// replies it upserts both subscriptions and bumps the room's last-message pointer.
// It returns the threadRoomID so the caller can pass it to SaveThreadMessage.
//
// `replier` may be nil for system messages with no real user (rare in thread
// paths); subscriptions for the replier are skipped in that case.
func (h *Handler) handleThreadRoomAndSubscriptions(ctx context.Context, msg *model.Message, eventSiteID string, replier *model.User) (string, error) {
	now := msg.CreatedAt

	var parentCreatedAt time.Time
	if msg.ThreadParentMessageCreatedAt != nil {
		parentCreatedAt = *msg.ThreadParentMessageCreatedAt
	}
	threadRoom := model.ThreadRoom{
		ID:                    idgen.GenerateUUIDv7(),
		ParentMessageID:       msg.ThreadParentMessageID,
		ThreadParentCreatedAt: parentCreatedAt,
		RoomID:                msg.RoomID,
		SiteID:                eventSiteID,
		LastMsgAt:             now,
		LastMsgID:             msg.ID,
		ReplyAccounts:         []string{msg.UserAccount},
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	err := h.threadStore.CreateThreadRoom(ctx, &threadRoom)
	switch {
	case err == nil:
		return threadRoom.ID, h.handleFirstThreadReply(ctx, msg, threadRoom.ID, replier, now)
	case errors.Is(err, errThreadRoomExists):
		return h.handleSubsequentThreadReply(ctx, msg, replier, now)
	default:
		return "", fmt.Errorf("create thread room: %w", err)
	}
}

// handleFirstThreadReply runs after the thread room has just been created.
// It inserts subscriptions for the parent author (looked up via userStore for
// the parent's home site) and, if distinct, for the replier (using the
// replier's home site).
func (h *Handler) handleFirstThreadReply(ctx context.Context, msg *model.Message, threadRoomID string, replier *model.User, now time.Time) error {
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

	parentSiteID, err := h.lookupOwnerSiteID(ctx, parentSender.ID, "first-reply parent")
	if err != nil {
		return fmt.Errorf("lookup parent owner site: %w", err)
	}
	if parentSiteID != "" {
		parentSub := h.buildThreadSubscription(msg, threadRoomID, parentSender.ID, parentSender.Account, parentSiteID, now)
		if err := h.threadStore.InsertThreadSubscription(ctx, parentSub); err != nil {
			return fmt.Errorf("insert parent author thread subscription: %w", err)
		}
		if err := h.publishThreadSubOutboxIfRemote(ctx, parentSub, msg.ID); err != nil {
			return err
		}
	}

	if replier != nil && msg.UserID != parentSender.ID {
		replierSub := h.buildThreadSubscription(msg, threadRoomID, msg.UserID, msg.UserAccount, replier.SiteID, now)
		if err := h.threadStore.InsertThreadSubscription(ctx, replierSub); err != nil {
			return fmt.Errorf("insert replier thread subscription: %w", err)
		}
		if err := h.publishThreadSubOutboxIfRemote(ctx, replierSub, msg.ID); err != nil {
			return err
		}
	}

	// Requires ThreadParentMessageCreatedAt to address the Cassandra row;
	// skipped when absent.
	if msg.ThreadParentMessageCreatedAt != nil {
		if err := h.store.UpdateParentMessageThreadRoomID(ctx, msg.ThreadParentMessageID, msg.RoomID, *msg.ThreadParentMessageCreatedAt, threadRoomID); err != nil {
			return fmt.Errorf("stamp thread_room_id on parent message: %w", err)
		}
	}

	return nil
}

// handleSubsequentThreadReply runs when CreateThreadRoom reported an existing room.
// Upserts subscriptions for both the parent author and the replier (idempotent
// on redelivery), then bumps the room's last-message pointer. Returns the
// existing thread room ID so the caller can pass it to SaveThreadMessage.
func (h *Handler) handleSubsequentThreadReply(ctx context.Context, msg *model.Message, replier *model.User, now time.Time) (string, error) {
	existingRoom, err := h.threadStore.GetThreadRoomByParentMessageID(ctx, msg.ThreadParentMessageID)
	if err != nil {
		return "", fmt.Errorf("get existing thread room: %w", err)
	}

	parentFound := true
	parentSender, err := h.store.GetMessageSender(ctx, msg.ThreadParentMessageID)
	switch {
	case err == nil:
		parentSiteID, lookupErr := h.lookupOwnerSiteID(ctx, parentSender.ID, "subsequent-reply parent")
		if lookupErr != nil {
			return "", fmt.Errorf("lookup parent owner site: %w", lookupErr)
		}
		if parentSiteID != "" {
			if err := h.threadStore.UpsertThreadSubscription(ctx,
				h.buildThreadSubscription(msg, existingRoom.ID, parentSender.ID, parentSender.Account, parentSiteID, now),
			); err != nil {
				return "", fmt.Errorf("upsert parent author thread subscription: %w", err)
			}
		}
		if replier != nil && msg.UserID != parentSender.ID {
			if err := h.threadStore.UpsertThreadSubscription(ctx,
				h.buildThreadSubscription(msg, existingRoom.ID, msg.UserID, msg.UserAccount, replier.SiteID, now),
			); err != nil {
				return "", fmt.Errorf("upsert replier thread subscription: %w", err)
			}
		}
	case errors.Is(err, errMessageNotFound):
		parentFound = false
		slog.Warn("thread reply parent not found — skipping parent subscription upsert",
			"parentMessageID", msg.ThreadParentMessageID,
			"replyID", msg.ID)
		if replier != nil {
			if err := h.threadStore.UpsertThreadSubscription(ctx,
				h.buildThreadSubscription(msg, existingRoom.ID, msg.UserID, msg.UserAccount, replier.SiteID, now),
			); err != nil {
				return "", fmt.Errorf("upsert replier thread subscription: %w", err)
			}
		}
	default:
		return "", fmt.Errorf("get parent message sender: %w", err)
	}

	if err := h.threadStore.UpdateThreadRoomLastMessage(ctx, existingRoom.ID, msg.ID, msg.UserAccount, now); err != nil {
		return "", fmt.Errorf("update thread room last message: %w", err)
	}

	// Re-stamp handles redelivery: first attempt may have created the thread room
	// but crashed before the stamp landed. IF EXISTS in the store prevents phantom rows.
	if parentFound && msg.ThreadParentMessageCreatedAt != nil {
		if err := h.store.UpdateParentMessageThreadRoomID(ctx, msg.ThreadParentMessageID, msg.RoomID, *msg.ThreadParentMessageCreatedAt, existingRoom.ID); err != nil {
			return "", fmt.Errorf("stamp thread_room_id on parent message: %w", err)
		}
	}

	return existingRoom.ID, nil
}

// lookupOwnerSiteID resolves a user's home site by ID.
// Returns ("", nil) when the user is not found (logs a warning) so callers
// can skip that user gracefully — parallels the errMessageNotFound branch
// already in this file. Other DB errors are returned for the caller to NAK on.
func (h *Handler) lookupOwnerSiteID(ctx context.Context, userID, role string) (string, error) {
	user, err := h.userStore.FindUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, userstore.ErrUserNotFound) {
			slog.Warn("owner user not found — skipping thread subscription",
				"userID", userID, "role", role)
			return "", nil
		}
		return "", fmt.Errorf("lookup user %s: %w", userID, err)
	}
	return user.SiteID, nil
}

// buildThreadSubscription constructs a ThreadSubscription for (threadRoomID, userID).
// ownerSiteID is the home site of the subscription's owner — NOT the room's site —
// so the document round-trips correctly through the OUTBOX/INBOX federation.
// lastSeenAt is always nil; the field is owned by user-action paths, not message-worker.
func (h *Handler) buildThreadSubscription(msg *model.Message, threadRoomID, userID, userAccount, ownerSiteID string, now time.Time) *model.ThreadSubscription {
	return &model.ThreadSubscription{
		ID:              idgen.GenerateUUIDv7(),
		ParentMessageID: msg.ThreadParentMessageID,
		RoomID:          msg.RoomID,
		ThreadRoomID:    threadRoomID,
		UserID:          userID,
		UserAccount:     userAccount,
		SiteID:          ownerSiteID,
		LastSeenAt:      nil,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// markThreadMentions flips hasMention=true on the thread subscription of every
// @account mentionee in msg (auto-creating the subscription if absent). The
// sender is excluded, and @all is ignored at the thread level. The
// subscription's SiteID is the mentionee's home site (carried on Participant).
func (h *Handler) markThreadMentions(ctx context.Context, msg *model.Message, threadRoomID, eventSiteID string) error {
	for i := range msg.Mentions {
		p := &msg.Mentions[i]
		if p.Account == "all" {
			continue
		}
		if p.UserID == msg.UserID {
			continue
		}
		ownerSiteID := p.SiteID
		if ownerSiteID == "" {
			// Defensive: should not happen since mention.Resolve populates SiteID
			// for resolved users. Fall back to event site so the local mark still
			// happens; outbox publish (Task 8) will skip on empty SiteID anyway.
			slog.Warn("mentionee participant has empty SiteID, falling back to event site",
				"account", p.Account, "userID", p.UserID, "msgID", msg.ID)
			ownerSiteID = eventSiteID
		}
		sub := h.buildThreadSubscription(msg, threadRoomID, p.UserID, p.Account, ownerSiteID, msg.CreatedAt)
		sub.HasMention = true
		if err := h.threadStore.MarkThreadSubscriptionMention(ctx, sub); err != nil {
			return fmt.Errorf("mark thread subscription mention for user %s: %w", p.UserID, err)
		}
	}
	return nil
}

// publishThreadSubOutboxIfRemote publishes a thread_subscription_upserted
// outbox event when sub.SiteID is a remote site. Same-site or empty SiteID
// is a no-op (empty SiteID logs a warning — it indicates a caller bug).
//
// The dedup-ID seed is (threadRoomID, userID, msgID): msg.ID is unique per
// reply, and (msg.ID, userID) is unique within a reply, so the seed is stable
// across MESSAGES_CANONICAL redeliveries and JetStream stream-level dedup
// absorbs duplicates within the dedup window.
func (h *Handler) publishThreadSubOutboxIfRemote(ctx context.Context, sub *model.ThreadSubscription, msgID string) error {
	if sub.SiteID == "" {
		slog.Warn("thread subscription has empty SiteID, skipping outbox publish",
			"threadRoomID", sub.ThreadRoomID, "userID", sub.UserID, "msgID", msgID)
		return nil
	}
	if sub.SiteID == h.siteID {
		return nil
	}

	payload, err := json.Marshal(sub)
	if err != nil {
		return fmt.Errorf("marshal thread subscription: %w", err)
	}
	outbox := model.OutboxEvent{
		Type:       model.OutboxThreadSubscriptionUpserted,
		SiteID:     h.siteID,
		DestSiteID: sub.SiteID,
		Payload:    payload,
		Timestamp:  time.Now().UTC().UnixMilli(),
	}
	data, err := json.Marshal(outbox)
	if err != nil {
		return fmt.Errorf("marshal outbox event: %w", err)
	}
	payloadSeed := fmt.Sprintf("thread-sub-outbox:%s:%s:%s", sub.ThreadRoomID, sub.UserID, msgID)
	dedupID := outboxDedupID(ctx, sub.SiteID, payloadSeed)
	subj := subject.Outbox(h.siteID, sub.SiteID, model.OutboxThreadSubscriptionUpserted)
	if err := h.publish(ctx, subj, data, dedupID); err != nil {
		return fmt.Errorf("publish thread subscription outbox to %s: %w", sub.SiteID, err)
	}
	return nil
}

// outboxDedupID composes Nats-Msg-Id as base+":"+destSiteID; base is X-Request-ID
// from ctx, falling back to payloadSeed when absent (partial-deployment safety).
// Mirrors room-worker's helper of the same shape so cross-site dedup IDs are
// consistent across services that publish to OUTBOX.
func outboxDedupID(ctx context.Context, destSiteID, payloadSeed string) string {
	base := natsutil.RequestIDFromContext(ctx)
	if base == "" {
		slog.Warn("missing X-Request-ID; falling back to payload-derived outbox dedup base",
			"destSiteID", destSiteID)
		base = payloadSeed
	}
	return base + ":" + destSiteID
}
