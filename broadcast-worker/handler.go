package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hmchangw/chat/pkg/mention"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomcrypto"
	"github.com/hmchangw/chat/pkg/roomkeystore"
	"github.com/hmchangw/chat/pkg/roommetacache"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/userstore"
)

// errNoCurrentKey is returned when a room has no encryption key in Valkey.
var errNoCurrentKey = errors.New("no current key")

// Publisher abstracts NATS publishing so the handler is testable.
type Publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

// RoomKeyProvider fetches the current encryption key for a room.
// Defined here (not imported from pkg/roomkeystore directly) to keep the
// handler's dependency contract narrow — only Get is used.
type RoomKeyProvider interface {
	Get(ctx context.Context, roomID string) (*roomkeystore.VersionedKeyPair, error)
}

// Handler processes MESSAGES_CANONICAL messages and broadcasts room events.
type Handler struct {
	store     Store
	userStore userstore.UserStore
	pub       Publisher
	keyStore  RoomKeyProvider
	encrypt   bool
	encoder   *roomcrypto.Encoder
}

func NewHandler(store Store, userStore userstore.UserStore, pub Publisher, keyStore RoomKeyProvider, encrypt bool) *Handler {
	return &Handler{
		store:     store,
		userStore: userStore,
		pub:       pub,
		keyStore:  keyStore,
		encrypt:   encrypt,
		encoder:   roomcrypto.NewEncoder(),
	}
}

// HandleMessage processes a single MESSAGES_CANONICAL message payload.
func (h *Handler) HandleMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	switch evt.Event {
	case model.EventCreated:
		return h.handleCreated(ctx, &evt)
	case model.EventUpdated:
		return h.handleUpdated(ctx, &evt)
	case model.EventDeleted:
		return h.handleDeleted(ctx, &evt)
	case model.EventThreadReplyAdded:
		return h.handleThreadTCountUpdated(ctx, &evt)
	default:
		slog.Warn("unknown message event type, skipping", "event", evt.Event, "messageID", evt.Message.ID)
		return nil
	}
}

// shouldUseThreadFanOut reports whether a message should be routed through the
// thread fan-out path (thread subscribers + @-mentions) rather than the room
// broadcast path. True when the message is a thread reply hidden from the main
// channel (TShow=false).
func shouldUseThreadFanOut(msg *model.Message) bool {
	return msg.ThreadParentMessageID != "" && !msg.TShow
}

func (h *Handler) handleCreated(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message

	if shouldUseThreadFanOut(&msg) {
		return h.handleThreadCreated(ctx, evt)
	}

	// One user-store round-trip covers both mention enrichment and sender
	// enrichment: parse mentions, dedupe with the sender, fetch once, then
	// hand the resulting map to ResolveFromParsed (skips a second parse) and
	// to buildClientMessage.
	parsed := mention.Parse(msg.Content)
	lookupAccounts := dedupedAccounts(msg.UserAccount, parsed.Accounts)
	users, lookupErr := h.userStore.FindUsersByAccounts(ctx, lookupAccounts)
	if lookupErr != nil {
		slog.Warn("user lookup failed, falling back to account", "error", lookupErr)
	}
	userByAccount := usersByAccount(users)

	resolved := mention.ResolveFromParsed(parsed, userByAccount)

	if err := h.store.UpdateRoomLastMessage(ctx, msg.RoomID, msg.ID, msg.CreatedAt, resolved.MentionAll); err != nil {
		return fmt.Errorf("update room last message %s: %w", msg.RoomID, err)
	}
	meta, err := h.store.GetRoomMeta(ctx, msg.RoomID)
	if err != nil {
		return fmt.Errorf("get room meta %s: %w", msg.RoomID, err)
	}

	if len(resolved.Accounts) > 0 {
		if err := h.store.SetSubscriptionMentions(ctx, meta.ID, resolved.Accounts); err != nil {
			return fmt.Errorf("set subscription mentions: %w", err)
		}
	}

	clientMsg := buildClientMessage(&msg, userByAccount)

	switch meta.Type {
	case model.RoomTypeChannel:
		return h.publishChannelEvent(ctx, meta, clientMsg, resolved.MentionAll, resolved.Participants)
	case model.RoomTypeDM, model.RoomTypeBotDM:
		return h.publishDMEvents(ctx, meta, clientMsg, resolved.Accounts)
	default:
		slog.Warn("unknown room type, skipping fan-out", "type", meta.Type, "roomID", meta.ID)
		return nil
	}
}

func (h *Handler) handleThreadCreated(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message
	parentMsgID := msg.ThreadParentMessageID

	parsed := mention.Parse(msg.Content)

	// Fetch room type first so DM/BotDM rooms skip the thread-subscription query
	// entirely — their fan-out uses ListSubscriptions, not thread subscribers.
	meta, err := h.store.GetRoomMeta(ctx, msg.RoomID)
	if err != nil {
		return fmt.Errorf("get room meta %s: %w", msg.RoomID, err)
	}

	// Channel rooms: only thread subscribers and @-mentioned accounts receive the
	// event. Fetch the subscriber list and build fanOut before any further work.
	var fanOut []string
	if meta.Type == model.RoomTypeChannel {
		fanOut, err = h.channelThreadFanOut(ctx, parentMsgID, msg.UserAccount, parsed.Accounts)
		if err != nil {
			return err
		}
		if len(fanOut) == 0 {
			slog.Debug("no thread subscribers to notify for thread reply",
				"parentMessageID", parentMsgID)
			return nil
		}
	}

	lookupAccounts := dedupedAccounts(msg.UserAccount, parsed.Accounts)
	users, lookupErr := h.userStore.FindUsersByAccounts(ctx, lookupAccounts)
	if lookupErr != nil {
		slog.Warn("user lookup failed for thread reply, falling back to account",
			"error", lookupErr, "parentMessageID", parentMsgID)
	}
	userByAccount := usersByAccount(users)

	resolved := mention.ResolveFromParsed(parsed, userByAccount)

	clientMsg := buildClientMessage(&msg, userByAccount)

	switch meta.Type {
	case model.RoomTypeChannel:
		// Do NOT call SetSubscriptionMentions here: TShow=false replies are invisible
		// in the main channel, so a room-level mention badge would appear with no
		// visible message to explain it.
		roomEvt := buildRoomEvent(meta, clientMsg)
		roomEvt.MentionAll = resolved.MentionAll
		if len(resolved.Participants) > 0 {
			roomEvt.Mentions = resolved.Participants
		}
		if err := h.encryptRoomEvent(ctx, meta.ID, clientMsg, &roomEvt); err != nil {
			return err
		}
		payload, err := json.Marshal(roomEvt)
		if err != nil {
			return fmt.Errorf("marshal thread created event for parent %s: %w", parentMsgID, err)
		}
		return h.publishToThreadAccounts(ctx, fanOut, payload, parentMsgID)
	case model.RoomTypeDM, model.RoomTypeBotDM:
		// DM thread replies are visible to all members, so @-mention badges are correct.
		if len(resolved.Accounts) > 0 {
			if err := h.store.SetSubscriptionMentions(ctx, meta.ID, resolved.Accounts); err != nil {
				return fmt.Errorf("set subscription mentions: %w", err)
			}
		}
		if err := h.store.UpdateRoomLastMessage(ctx, msg.RoomID, msg.ID, msg.CreatedAt, resolved.MentionAll); err != nil {
			return fmt.Errorf("update room last message %s: %w", msg.RoomID, err)
		}
		return h.publishDMEvents(ctx, meta, clientMsg, resolved.Accounts)
	default:
		slog.Warn("unknown room type, skipping thread fan-out", "type", meta.Type, "roomID", meta.ID)
		return nil
	}
}

func (h *Handler) handleUpdated(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message
	if msg.EditedAt == nil || msg.UpdatedAt == nil {
		return fmt.Errorf("updated event missing EditedAt or UpdatedAt: %s", msg.ID)
	}

	if shouldUseThreadFanOut(&msg) {
		return h.handleThreadUpdated(ctx, evt)
	}

	room, err := h.store.GetRoom(ctx, msg.RoomID)
	if err != nil {
		return fmt.Errorf("fetch room %s: %w", msg.RoomID, err)
	}

	edit := buildEditRoomEvent(room, evt)
	if room.Type == model.RoomTypeChannel && h.encrypt {
		if err := h.encryptEditedContent(ctx, room.ID, &edit); err != nil {
			return err
		}
	}
	return h.publishMutation(ctx, room, model.RoomEventMessageEdited, msg.ID, &edit)
}

func (h *Handler) handleThreadUpdated(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message
	if msg.EditedAt == nil || msg.UpdatedAt == nil {
		return fmt.Errorf("updated event missing EditedAt or UpdatedAt for thread reply %s", msg.ID)
	}
	parentMsgID := msg.ThreadParentMessageID

	// GetRoom (not GetRoomMeta) so the DM/BotDM branch has room.Accounts for
	// fan-out. Fetched first so the routing decision is made before any
	// thread-follower lookup.
	room, err := h.store.GetRoom(ctx, msg.RoomID)
	if err != nil {
		return fmt.Errorf("get room %s: %w", msg.RoomID, err)
	}

	edit := buildEditRoomEvent(room, evt)

	switch room.Type {
	case model.RoomTypeChannel:
		parsed := mention.Parse(msg.Content)
		fanOut, err := h.channelThreadFanOut(ctx, parentMsgID, msg.UserAccount, parsed.Accounts)
		if err != nil {
			return err
		}
		if len(fanOut) == 0 {
			slog.Debug("no thread subscribers to notify for thread update",
				"parentMessageID", parentMsgID)
			return nil
		}
		if h.encrypt {
			if err := h.encryptEditedContent(ctx, room.ID, &edit); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(&edit)
		if err != nil {
			return fmt.Errorf("marshal thread edit event for parent %s: %w", parentMsgID, err)
		}
		return h.publishToThreadAccounts(ctx, fanOut, payload, parentMsgID)
	case model.RoomTypeDM, model.RoomTypeBotDM:
		// DM thread replies are visible to every member, so edits fan out to
		// all members (consistent with handleThreadCreated), not just thread
		// subscribers.
		return h.publishMutation(ctx, room, model.RoomEventMessageEdited, msg.ID, &edit)
	default:
		slog.Warn("unknown room type, skipping thread update fan-out", "type", room.Type, "roomID", room.ID)
		return nil
	}
}

func (h *Handler) handleThreadDeleted(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message
	parentMsgID := msg.ThreadParentMessageID

	if msg.UpdatedAt == nil {
		return fmt.Errorf("missing UpdatedAt for thread message %s", msg.ID)
	}

	// GetRoom first so the routing decision (thread followers vs all DM
	// members) is made from the authoritative room type and Accounts.
	room, err := h.store.GetRoom(ctx, msg.RoomID)
	if err != nil {
		return fmt.Errorf("get room %s: %w", msg.RoomID, err)
	}

	del := buildDeleteRoomEvent(room, evt)

	switch room.Type {
	case model.RoomTypeChannel:
		// Parse @-mentions from the deleted message so that non-follower
		// recipients who received the create event (via mention fan-out) also
		// receive the delete. Only the channel path uses mentions; the DM path
		// fans out to all members.
		parsed := mention.Parse(msg.Content)
		fanOut, err := h.channelThreadFanOut(ctx, parentMsgID, msg.UserAccount, parsed.Accounts)
		if err != nil {
			return err
		}
		if len(fanOut) > 0 {
			payload, err := json.Marshal(&del)
			if err != nil {
				return fmt.Errorf("marshal thread delete event for parent %s: %w", parentMsgID, err)
			}
			if err := h.publishToThreadAccounts(ctx, fanOut, payload, parentMsgID); err != nil {
				return err
			}
		}
	case model.RoomTypeDM, model.RoomTypeBotDM:
		// DM thread replies are visible to every member, so deletes fan out to
		// all members (consistent with handleThreadCreated), not just thread
		// subscribers.
		if err := h.publishMutation(ctx, room, model.RoomEventMessageDeleted, msg.ID, &del); err != nil {
			return err
		}
	default:
		slog.Warn("unknown room type, skipping thread delete fan-out", "type", room.Type, "roomID", room.ID)
		// No return: the badge update below is safe for all room types;
		// publishThreadMetadata handles unknown types by logging and skipping.
	}

	// Badge (tcount) update applies to all room types.
	if evt.NewTCount != nil {
		h.publishThreadBadge(ctx, room, *evt.NewTCount, parentMsgID, msg.ID)
	}

	return nil
}

func (h *Handler) handleThreadTCountUpdated(ctx context.Context, evt *model.MessageEvent) error {
	if evt.NewTCount == nil {
		slog.Warn("thread_reply_added event missing NewTCount, skipping", "messageID", evt.Message.ID)
		return nil
	}
	if evt.Message.ThreadParentMessageID == "" {
		slog.Warn("thread_reply_added event missing ThreadParentMessageID, skipping", "messageID", evt.Message.ID)
		return nil
	}
	room, err := h.store.GetRoom(ctx, evt.Message.RoomID)
	if err != nil {
		return fmt.Errorf("get room %s: %w", evt.Message.RoomID, err)
	}
	return h.publishThreadMetadata(ctx, room, *evt.NewTCount, evt.Message.ThreadParentMessageID, evt.Message.ID, model.ThreadActionReplyAdded)
}

func (h *Handler) publishThreadMetadata(ctx context.Context, room *model.Room, newTcount int,
	parentMsgID, replyMsgID string, action model.ThreadAction) error {
	evt := model.ThreadMetadataUpdatedEvent{
		Type:            model.RoomEventThreadMetadataUpdated,
		RoomID:          room.ID,
		SiteID:          room.SiteID,
		ParentMessageID: parentMsgID,
		ReplyMessageID:  replyMsgID,
		NewTCount:       newTcount,
		Action:          action,
		Timestamp:       time.Now().UTC().UnixMilli(),
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal thread metadata event for room %s: %w", room.ID, err)
	}
	switch room.Type {
	case model.RoomTypeChannel:
		if err := h.pub.Publish(ctx, subject.RoomEvent(room.ID), payload); err != nil {
			return fmt.Errorf("publish thread metadata for channel room %s: %w", room.ID, err)
		}
	case model.RoomTypeDM, model.RoomTypeBotDM:
		for _, account := range room.Accounts {
			if isBot(account) {
				continue
			}
			if err := h.pub.Publish(ctx, subject.UserRoomEvent(account), payload); err != nil {
				return fmt.Errorf("publish thread metadata to DM member %s in room %s: %w", account, room.ID, err)
			}
		}
	default:
		slog.Warn("unknown room type for thread metadata, skipping", "type", room.Type, "roomID", room.ID)
	}
	return nil
}

func (h *Handler) handleDeleted(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message
	if msg.UpdatedAt == nil {
		return fmt.Errorf("deleted event missing UpdatedAt: %s", msg.ID)
	}

	if shouldUseThreadFanOut(&msg) {
		return h.handleThreadDeleted(ctx, evt)
	}

	room, err := h.store.GetRoom(ctx, msg.RoomID)
	if err != nil {
		return fmt.Errorf("fetch room %s: %w", msg.RoomID, err)
	}

	del := buildDeleteRoomEvent(room, evt)
	if err := h.publishMutation(ctx, room, model.RoomEventMessageDeleted, msg.ID, &del); err != nil {
		return err
	}
	// TShow=true thread replies appear in the main room (handled by publishMutation
	// above) but still count toward the thread's reply-count badge. Since
	// handleThreadDeleted is bypassed for TShow=true, we publish the badge update here.
	if msg.ThreadParentMessageID != "" && evt.NewTCount != nil {
		h.publishThreadBadge(ctx, room, *evt.NewTCount, msg.ThreadParentMessageID, msg.ID)
	}
	return nil
}

// publishThreadBadge publishes a thread-metadata badge update for a deleted
// reply. Errors are logged but not returned: badge updates are best-effort and
// JetStream will redeliver the parent event on failure.
func (h *Handler) publishThreadBadge(ctx context.Context, room *model.Room, newTCount int, parentMsgID, replyMsgID string) {
	if err := h.publishThreadMetadata(ctx, room, newTCount, parentMsgID, replyMsgID, model.ThreadActionReplyDeleted); err != nil {
		slog.Error("publish thread badge for deleted reply failed",
			"error", err, "parentMessageID", parentMsgID)
	}
}

// publishMutation marshals a flattened edit/delete event and routes it by room
// type: channel events go to the room stream, DM/botDM events fan out per
// non-bot member. evt must marshal to the wire payload for roomEvtType.
func (h *Handler) publishMutation(ctx context.Context, room *model.Room, roomEvtType model.RoomEventType, messageID string, evt any) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", roomEvtType, err)
	}

	switch room.Type {
	case model.RoomTypeChannel:
		if err := h.pub.Publish(ctx, subject.RoomEvent(room.ID), payload); err != nil {
			return fmt.Errorf("publish %s event for room %s message %s: %w", roomEvtType, room.ID, messageID, err)
		}
		return nil

	case model.RoomTypeDM, model.RoomTypeBotDM:
		for _, account := range room.Accounts {
			if isBot(account) {
				continue
			}
			if err := h.pub.Publish(ctx, subject.UserRoomEvent(account), payload); err != nil {
				slog.Error("publish DM mutation event failed",
					"error", err,
					"type", roomEvtType,
					"account", account,
					"messageID", messageID,
					"roomID", room.ID,
				)
			}
		}
		return nil

	default:
		slog.Warn("unknown room type, skipping mutation fan-out", "type", room.Type, "roomID", room.ID)
		return nil
	}
}

func buildEditRoomEvent(room *model.Room, evt *model.MessageEvent) model.EditRoomEvent {
	msg := evt.Message
	return model.EditRoomEvent{
		Type:       model.RoomEventMessageEdited,
		RoomID:     room.ID,
		SiteID:     room.SiteID,
		Timestamp:  evt.Timestamp,
		MessageID:  msg.ID,
		NewContent: msg.Content,
		EditedBy:   msg.UserAccount,
		EditedAt:   *msg.EditedAt,
		UpdatedAt:  *msg.UpdatedAt,
	}
}

func buildDeleteRoomEvent(room *model.Room, evt *model.MessageEvent) model.DeleteRoomEvent {
	msg := evt.Message
	return model.DeleteRoomEvent{
		Type:      model.RoomEventMessageDeleted,
		RoomID:    room.ID,
		SiteID:    room.SiteID,
		Timestamp: evt.Timestamp,
		MessageID: msg.ID,
		DeletedBy: msg.UserAccount,
		DeletedAt: *msg.UpdatedAt,
		UpdatedAt: *msg.UpdatedAt,
	}
}

func (h *Handler) encryptEditedContent(ctx context.Context, roomID string, edited *model.EditRoomEvent) error {
	key, err := h.currentRoomKey(ctx, roomID)
	if err != nil {
		return err
	}
	encrypted, err := h.encoder.Encode(roomID, edited.NewContent, key.KeyPair.PrivateKey, key.Version)
	if err != nil {
		return fmt.Errorf("encrypt edit content for room %s: %w", roomID, err)
	}
	encJSON, err := json.Marshal(encrypted)
	if err != nil {
		return fmt.Errorf("marshal encrypted edit content: %w", err)
	}
	edited.EncryptedNewContent = json.RawMessage(encJSON)
	edited.NewContent = ""
	return nil
}

// currentRoomKey fetches the room's encryption key, treating a missing key as
// an error (the room is configured for encryption but no key is provisioned).
func (h *Handler) currentRoomKey(ctx context.Context, roomID string) (*roomkeystore.VersionedKeyPair, error) {
	key, err := h.keyStore.Get(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("get room key for room %s: %w", roomID, err)
	}
	if key == nil {
		return nil, fmt.Errorf("get room key for room %s: %w", roomID, errNoCurrentKey)
	}
	return key, nil
}

// encryptRoomEvent applies room encryption to evt if h.encrypt is true,
// replacing evt.Message with an EncryptedMessage envelope built from clientMsg.
func (h *Handler) encryptRoomEvent(ctx context.Context, roomID string, clientMsg *model.ClientMessage, evt *model.RoomEvent) error {
	if !h.encrypt {
		return nil
	}
	msgJSON, err := json.Marshal(clientMsg)
	if err != nil {
		return fmt.Errorf("marshal client message for room %s: %w", roomID, err)
	}
	key, err := h.currentRoomKey(ctx, roomID)
	if err != nil {
		return err
	}
	encrypted, err := h.encoder.Encode(roomID, string(msgJSON), key.KeyPair.PrivateKey, key.Version)
	if err != nil {
		return fmt.Errorf("encrypt message for room %s: %w", roomID, err)
	}
	encJSON, err := json.Marshal(encrypted)
	if err != nil {
		return fmt.Errorf("marshal encrypted message for room %s: %w", roomID, err)
	}
	evt.EncryptedMessage = json.RawMessage(encJSON)
	evt.Message = nil
	return nil
}

func (h *Handler) publishChannelEvent(ctx context.Context, meta roommetacache.Meta, clientMsg *model.ClientMessage, mentionAll bool, mentions []model.Participant) error {
	evt := buildRoomEvent(meta, clientMsg)
	evt.MentionAll = mentionAll
	if len(mentions) > 0 {
		evt.Mentions = mentions
	}
	if err := h.encryptRoomEvent(ctx, meta.ID, clientMsg, &evt); err != nil {
		return err
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal channel event: %w", err)
	}
	return h.pub.Publish(ctx, subject.RoomEvent(meta.ID), payload)
}

func (h *Handler) publishDMEvents(ctx context.Context, meta roommetacache.Meta, clientMsg *model.ClientMessage, mentionedAccounts []string) error {
	subs, err := h.store.ListSubscriptions(ctx, meta.ID)
	if err != nil {
		return fmt.Errorf("list subscriptions for DM room %s: %w", meta.ID, err)
	}

	mentionSet := make(map[string]struct{}, len(mentionedAccounts))
	for _, name := range mentionedAccounts {
		mentionSet[name] = struct{}{}
	}

	for i := range subs {
		account := subs[i].User.Account
		// Skip bots: live UI events go to human clients only, consistent with
		// publishMutation and publishThreadMetadata. Bots receive messages via
		// their own server-side integration, not the websocket event channel.
		if isBot(account) {
			continue
		}
		_, hasMention := mentionSet[account]

		evt := buildRoomEvent(meta, clientMsg)
		evt.HasMention = hasMention

		payload, err := json.Marshal(evt)
		if err != nil {
			return fmt.Errorf("marshal DM event for user %s: %w", account, err)
		}
		if err := h.pub.Publish(ctx, subject.UserRoomEvent(account), payload); err != nil {
			slog.Error("publish DM event failed", "error", err, "account", account)
		}
	}
	return nil
}

func buildRoomEvent(meta roommetacache.Meta, clientMsg *model.ClientMessage) model.RoomEvent {
	return model.RoomEvent{
		Type:      model.RoomEventNewMessage,
		RoomID:    meta.ID,
		Timestamp: time.Now().UTC().UnixMilli(),
		RoomName:  meta.Name,
		RoomType:  meta.Type,
		SiteID:    meta.SiteID,
		UserCount: meta.UserCount,
		LastMsgAt: clientMsg.CreatedAt,
		LastMsgID: clientMsg.ID,
		Message:   clientMsg,
	}
}

func buildClientMessage(msg *model.Message, userMap map[string]model.User) *model.ClientMessage {
	sender := model.Participant{
		UserID:  msg.UserID,
		Account: msg.UserAccount,
	}
	if u, ok := userMap[msg.UserAccount]; ok {
		sender.ChineseName = u.ChineseName
		sender.EngName = u.EngName
	} else {
		sender.ChineseName = msg.UserAccount
		sender.EngName = msg.UserAccount
	}
	return &model.ClientMessage{
		Message: *msg,
		Sender:  &sender,
	}
}

// publishToThreadAccounts publishes payload to every account in the list. On the first
// publish failure it logs and returns the error so the caller can propagate it to
// JetStream for redelivery — thread per-user events must have the same retry guarantee
// as room-channel events (which propagate errors via publishMutation).
func (h *Handler) publishToThreadAccounts(ctx context.Context, accounts []string, payload []byte, parentMsgID string) error {
	for _, account := range accounts {
		if err := h.pub.Publish(ctx, subject.UserRoomEvent(account), payload); err != nil {
			slog.Error("publish thread event failed",
				"error", err, "account", account, "parentMessageID", parentMsgID)
			return fmt.Errorf("publish thread event to %s for parent %s: %w", account, parentMsgID, err)
		}
	}
	return nil
}

// threadFanOutAccounts builds the deduplicated fan-out recipient list for
// a thread event. senderAccount is always excluded. extraAccounts
// (e.g. @mentioned users from the message payload) are added after the
// follower pass.
func threadFanOutAccounts(senderAccount string, followers map[string]struct{}, extraAccounts []string) []string {
	seen := map[string]struct{}{senderAccount: {}}
	var fanOut []string
	for acc := range followers {
		if _, ok := seen[acc]; ok {
			continue
		}
		if isBot(acc) {
			continue
		}
		seen[acc] = struct{}{}
		fanOut = append(fanOut, acc)
	}
	for _, acc := range extraAccounts {
		if _, ok := seen[acc]; ok {
			continue
		}
		if isBot(acc) {
			continue
		}
		seen[acc] = struct{}{}
		fanOut = append(fanOut, acc)
	}
	return fanOut
}

// channelThreadFanOut resolves the deduplicated recipient list for a channel
// thread event: it fetches the parent message's thread followers and merges
// them with the @-mentioned accounts, excluding the sender. Shared by the
// channel branch of every thread handler (created/updated/deleted).
func (h *Handler) channelThreadFanOut(ctx context.Context, parentMsgID, sender string, mentions []string) ([]string, error) {
	followers, err := h.store.GetThreadFollowers(ctx, parentMsgID)
	if err != nil {
		return nil, fmt.Errorf("get thread followers for parent %s: %w", parentMsgID, err)
	}
	return threadFanOutAccounts(sender, followers, mentions), nil
}

// usersByAccount indexes a slice of users by their Account for O(1) lookup
// during mention resolution and client-message enrichment.
func usersByAccount(users []model.User) map[string]model.User {
	byAccount := make(map[string]model.User, len(users))
	for i := range users {
		byAccount[users[i].Account] = users[i]
	}
	return byAccount
}
