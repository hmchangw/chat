package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
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

	_ = requestID
	_, _ = requester, other
	return nil, errInvalidSyncDMRequest // placeholder — replaced in Task 8
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
