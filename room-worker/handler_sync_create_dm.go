package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

var (
	errMissingRequestID     = errors.New("missing X-Request-ID header")
	errInvalidRequestID     = errors.New("invalid X-Request-ID header")
	errInvalidSyncDMRequest = errors.New("invalid sync DM request")
	errUserLookupFailed     = errors.New("user lookup failed")
	errCrossSiteRequester   = errors.New("requester is not on this site")
	errRoomIDCollision      = errors.New("room ID collision (existing room metadata mismatch)")
)

// sanitizeSyncDMError maps a handler error to a user-displayable string.
// Known sentinels surface their literal message; anything else becomes "internal error"
// to avoid leaking raw error text (e.g. mongo or NATS internals).
func sanitizeSyncDMError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, errMissingRequestID),
		errors.Is(err, errInvalidRequestID),
		errors.Is(err, errInvalidSyncDMRequest),
		errors.Is(err, errUserLookupFailed),
		errors.Is(err, errCrossSiteRequester),
		errors.Is(err, errRoomIDCollision):
		return err.Error()
	default:
		return "internal error"
	}
}

// handleSyncCreateDM is the business logic for the sync DM endpoint. It takes the inbound
// request bytes and returns either the marshalled SyncCreateDMReply payload or an error.
func (h *Handler) handleSyncCreateDM(ctx context.Context, data []byte) ([]byte, error) {
	requestID := natsutil.RequestIDFromContext(ctx)
	if requestID == "" {
		return nil, errMissingRequestID
	}
	if !idgen.IsValidUUID(requestID) {
		return nil, errInvalidRequestID
	}

	var req model.SyncCreateDMRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, errInvalidSyncDMRequest
	}
	if err := validateSyncCreateDMShape(&req); err != nil {
		return nil, err
	}

	requester, err := h.store.GetUser(ctx, req.RequesterAccount)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, errUserLookupFailed
		}
		return nil, fmt.Errorf("get requester: %w", errUserLookupFailed)
	}
	if requester.SiteID != h.siteID {
		return nil, errCrossSiteRequester
	}

	other, err := h.store.GetUser(ctx, req.OtherAccount)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, errUserLookupFailed
		}
		return nil, fmt.Errorf("get counterpart: %w", errUserLookupFailed)
	}

	acceptedAt := time.Now().UTC()
	roomID := idgen.BuildDMRoomID(requester.ID, other.ID)

	room := &model.Room{
		ID:        roomID,
		Name:      "",
		Type:      req.RoomType,
		CreatedBy: requester.ID,
		SiteID:    h.siteID,
		CreatedAt: acceptedAt,
		UpdatedAt: acceptedAt,
	}
	if err := h.store.CreateRoom(ctx, room); err != nil {
		if !mongo.IsDuplicateKeyError(err) {
			return nil, fmt.Errorf("create room: %w", err)
		}
		existing, fetchErr := h.store.GetRoom(ctx, room.ID)
		if fetchErr != nil {
			return nil, fmt.Errorf("fetch room on duplicate-key: %w", fetchErr)
		}
		if existing.Type != room.Type ||
			existing.SiteID != room.SiteID ||
			existing.Name != room.Name ||
			existing.CreatedBy != room.CreatedBy {
			return nil, errRoomIDCollision
		}
		room = existing
		acceptedAt = existing.CreatedAt
	}

	var subs []*model.Subscription
	switch req.RoomType {
	case model.RoomTypeDM:
		subs = buildDMSubs(requester, other, room, acceptedAt)
	case model.RoomTypeBotDM:
		subs = buildBotDMSubs(requester, other, room, acceptedAt)
	default:
		return nil, errInvalidSyncDMRequest
	}

	requesterSub, err := h.persistSyncDMSubs(ctx, requester, other, subs)
	if err != nil {
		return nil, err
	}

	if rcErr := h.store.ReconcileMemberCounts(ctx, room.ID); rcErr != nil {
		slog.Error("sync DM: reconcile member counts failed",
			"error", rcErr, "roomID", room.ID, "requestID", requestID)
	}

	h.publishSubscriptionUpdates(ctx, subs, acceptedAt, requestID)

	if err := h.publishSyncDMOutbox(ctx, room, requester, other, acceptedAt, requestID); err != nil {
		slog.Error("sync DM: publish outbox failed",
			"error", err, "roomID", room.ID, "requestID", requestID)
	}

	reply, err := json.Marshal(model.SyncCreateDMReply{
		Success:      true,
		Subscription: *requesterSub,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal reply: %w", err)
	}
	return reply, nil
}

// persistSyncDMSubs inserts both subs. On a duplicate-key race (concurrent caller or retry),
// it falls back to fetching the requester's existing sub via FindDMSubscription and returns
// that — preserving idempotent behavior.
func (h *Handler) persistSyncDMSubs(ctx context.Context, requester, other *model.User,
	subs []*model.Subscription,
) (*model.Subscription, error) {
	err := h.store.BulkCreateSubscriptions(ctx, subs)
	if err == nil {
		return pickRequesterSub(subs, requester.Account), nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return nil, fmt.Errorf("bulk create subs: %w", err)
	}
	existing, fetchErr := h.store.FindDMSubscription(ctx, requester.Account, other.Account)
	if fetchErr != nil {
		return nil, fmt.Errorf("find existing sub on duplicate-key: %w", fetchErr)
	}
	return existing, nil
}

func pickRequesterSub(subs []*model.Subscription, requesterAccount string) *model.Subscription {
	for _, s := range subs {
		if s.User.Account == requesterAccount {
			return s
		}
	}
	return nil
}

func validateSyncCreateDMShape(req *model.SyncCreateDMRequest) error {
	switch req.RoomType {
	case model.RoomTypeDM, model.RoomTypeBotDM:
	default:
		return errInvalidSyncDMRequest
	}
	if req.RequesterAccount == "" || req.OtherAccount == "" {
		return errInvalidSyncDMRequest
	}
	if req.RequesterAccount == req.OtherAccount {
		return errInvalidSyncDMRequest
	}
	return nil
}

func (h *Handler) publishSubscriptionUpdates(ctx context.Context, subs []*model.Subscription, acceptedAt time.Time, requestID string) {
	for _, sub := range subs {
		evt := model.SubscriptionUpdateEvent{
			UserID:       sub.User.ID,
			Subscription: *sub,
			Action:       "added",
			Timestamp:    acceptedAt.UnixMilli(),
		}
		data, err := json.Marshal(evt)
		if err != nil {
			slog.Error("sync DM: marshal subscription.update failed",
				"error", err, "account", sub.User.Account, "requestID", requestID)
			continue
		}
		if err := h.publish(ctx, subject.SubscriptionUpdate(sub.User.Account), data, ""); err != nil {
			slog.Error("sync DM: publish subscription.update failed",
				"error", err, "account", sub.User.Account, "requestID", requestID)
		}
	}
}

func (h *Handler) publishSyncDMOutbox(ctx context.Context, room *model.Room, requester, other *model.User, acceptedAt time.Time, requestID string) error {
	if other.SiteID == "" || other.SiteID == h.siteID {
		return nil
	}

	payload := model.RoomCreatedOutbox{
		RoomID:           room.ID,
		RoomType:         room.Type,
		RoomName:         "",
		HomeSiteID:       room.SiteID,
		Accounts:         []string{other.Account},
		RequesterAccount: requester.Account,
		Timestamp:        acceptedAt.UnixMilli(),
	}
	pData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal room_created outbox payload: %w", err)
	}
	envelope := model.OutboxEvent{
		Type:       model.OutboxTypeRoomCreated,
		SiteID:     room.SiteID,
		DestSiteID: other.SiteID,
		Payload:    pData,
		Timestamp:  acceptedAt.UnixMilli(),
	}
	eData, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal outbox envelope: %w", err)
	}
	return h.publish(ctx,
		subject.Outbox(room.SiteID, other.SiteID, model.OutboxTypeRoomCreated),
		eData,
		requestID+":"+other.SiteID,
	)
}

// natsServerCreateDM is the NATS-side entry point for chat.server.request.room.{siteID}.create.dm.
// It builds the handler context, invokes handleSyncCreateDM, and replies via natsutil.
func (h *Handler) natsServerCreateDM(m otelnats.Msg) {
	ctx := natsutil.ContextWithRequestIDFromHeaders(m.Context(), m.Msg.Header)
	reply, err := h.handleSyncCreateDM(ctx, m.Msg.Data)
	if err != nil {
		natsutil.ReplyError(m.Msg, sanitizeSyncDMError(err))
		return
	}
	if err := m.Msg.Respond(reply); err != nil {
		slog.Error("sync DM: reply failed", "error", err)
	}
}
