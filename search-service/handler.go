package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/subject"
)

// handlerConfig carries knobs read per request. `RequestTimeout` of zero
// disables the context deadline so tests can skip wiring it.
type handlerConfig struct {
	DocCounts               int
	MaxDocCounts            int
	RestrictedRoomsCacheTTL time.Duration
	RecentWindow            time.Duration
	RequestTimeout          time.Duration
	UserRoomIndex           string
	SpotlightReadPattern    string
}

type handler struct {
	store SearchStore
	mongo MongoStore
	users SearchUsersClient
	cache RestrictedRoomCache
	cfg   handlerConfig
}

func newHandler(store SearchStore, mongo MongoStore, users SearchUsersClient, cache RestrictedRoomCache, cfg handlerConfig) *handler {
	if cfg.DocCounts <= 0 {
		cfg.DocCounts = 25
	}
	if cfg.MaxDocCounts <= 0 {
		cfg.MaxDocCounts = 100
	}
	if cfg.RestrictedRoomsCacheTTL <= 0 {
		cfg.RestrictedRoomsCacheTTL = 5 * time.Minute
	}
	if cfg.RecentWindow <= 0 {
		cfg.RecentWindow = 365 * 24 * time.Hour
	}
	return &handler{store: store, mongo: mongo, users: users, cache: cache, cfg: cfg}
}

func (h *handler) Register(r *natsrouter.Router) {
	natsrouter.Register(r, subject.SearchMessagesPattern(), h.searchMessages)
	natsrouter.Register(r, subject.SearchSubscriptionsPattern(), h.searchSubscriptions)
	natsrouter.Register(r, subject.SearchAppsPattern(), h.searchApps)
	natsrouter.Register(r, subject.SearchUsersPattern(), h.searchUsers)
}

func (h *handler) withRequestTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if h.cfg.RequestTimeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, h.cfg.RequestTimeout)
}

func (h *handler) searchMessages(c *natsrouter.Context, req model.SearchMessagesRequest) (resp *model.SearchMessagesResponse, err error) {
	defer observeRequest(metricKindMessages, &err)()

	account, rerr := c.Params.Require("account")
	if rerr != nil {
		return nil, rerr
	}

	if err := h.normalizePagination(&req.Size, &req.Offset); err != nil {
		return nil, err
	}
	if req.SearchText == "" {
		return nil, natsrouter.ErrBadRequest("searchText is required")
	}

	ctx, cancel := h.withRequestTimeout(c)
	defer cancel()

	restricted, err := h.loadRestricted(ctx, account)
	if err != nil {
		return nil, err
	}

	// `restricted` is the caller's full restrictedRooms map sourced from the
	// ES user-room-mv index (cached in Valkey by loadRestricted). It is the
	// single source of truth for restricted vs unrestricted classification.
	// When req.RoomIDs is set, buildMessageQuery -> scopedAccessClauses
	// iterates req.RoomIDs and classifies each ID against this map directly,
	// so no handler-level pre-classification is needed.
	body, err := buildMessageQuery(req, account, restricted, h.cfg.RecentWindow, h.cfg.UserRoomIndex)
	if err != nil {
		slog.Error("build message query failed", "account", account, "error", err)
		return nil, natsrouter.ErrInternal("unable to build search query")
	}

	observeESDone := observeES()
	raw, err := h.store.Search(ctx, MessageIndexPattern, body)
	observeESDone()
	if err != nil {
		slog.Error("message search backend failed", "account", account, "error", err)
		return nil, natsrouter.ErrInternal("search backend unavailable")
	}

	hits, total, err := parseMessagesResponse(raw)
	if err != nil {
		slog.Error("parse messages response failed", "account", account, "error", err)
		return nil, natsrouter.ErrInternal("unexpected search response")
	}

	if len(hits) == 0 {
		return &model.SearchMessagesResponse{
			Messages: []model.SearchMessage{},
			Total:    total,
		}, nil
	}

	// --- Mongo enrichment ---
	// Collect distinct userIDs and roomIDs from the ES hits.
	userIDSet := make(map[string]struct{}, len(hits))
	roomIDSet := make(map[string]struct{}, len(hits))
	for i := range hits {
		if hits[i].UserID != "" {
			userIDSet[hits[i].UserID] = struct{}{}
		}
		if hits[i].RoomID != "" {
			roomIDSet[hits[i].RoomID] = struct{}{}
		}
	}
	userIDs := setToSlice(userIDSet)
	roomIDs := setToSlice(roomIDSet)

	// User and room batch lookups are independent — fire them in parallel
	// to halve the enrichment-step latency. errgroup propagates the first
	// error and cancels the sibling.
	var (
		users []model.User
		rooms []model.Room
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		users, err = h.mongo.FindUsersByIDs(gctx, userIDs)
		return err
	})
	g.Go(func() error {
		var err error
		rooms, err = h.mongo.FindRoomsByIDs(gctx, roomIDs)
		return err
	})
	if err := g.Wait(); err != nil {
		slog.Error("message enrichment failed", "account", account, "error", err)
		return nil, natsrouter.ErrInternal("search enrichment unavailable")
	}

	// Build lookup maps for O(1) enrichment per hit.
	userMap := make(map[string]*model.User, len(users))
	for i := range users {
		userMap[users[i].ID] = &users[i]
	}
	roomMap := make(map[string]*model.Room, len(rooms))
	for i := range rooms {
		roomMap[rooms[i].ID] = &rooms[i]
	}

	// Zip hits with enrichment.
	messages := make([]model.SearchMessage, 0, len(hits))
	for i := range hits {
		messages = append(messages, toSearchMessage(&hits[i], userMap[hits[i].UserID], roomMap[hits[i].RoomID]))
	}

	return &model.SearchMessagesResponse{Messages: messages, Total: total}, nil
}

// setToSlice converts a set (map[string]struct{}) to a string slice.
// Used to deduplicate IDs before batch Mongo fetches.
func setToSlice(m map[string]struct{}) []string {
	s := make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	return s
}

func (h *handler) searchSubscriptions(c *natsrouter.Context, req model.SearchSubscriptionsRequest) (resp *model.SearchSubscriptionsResponse, err error) {
	defer observeRequest(metricKindSubscriptions, &err)()

	account, rerr := c.Params.Require("account")
	if rerr != nil {
		return nil, rerr
	}

	if err := h.normalizePagination(&req.Size, &req.Offset); err != nil {
		return nil, err
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, natsrouter.ErrBadRequest("query is required")
	}
	req.Query = query

	ctx, cancel := h.withRequestTimeout(c)
	defer cancel()

	body, err := buildSubscriptionQuery(req, account)
	if err != nil {
		// RouteError (invalid roomType) passes through;
		// anything else (marshal failure — unreachable) gets sanitized.
		var rerr *natsrouter.RouteError
		if errors.As(err, &rerr) {
			return nil, err
		}
		slog.Error("build subscription query failed", "account", account, "error", err)
		return nil, natsrouter.ErrInternal("unable to build search query")
	}

	observeESDone := observeES()
	raw, err := h.store.Search(ctx, []string{h.cfg.SpotlightReadPattern}, body)
	observeESDone()
	if err != nil {
		slog.Error("subscription search backend failed", "account", account, "error", err)
		return nil, natsrouter.ErrInternal("search backend unavailable")
	}

	roomIDs, err := parseSubscriptionRoomIDs(raw)
	if err != nil {
		slog.Error("parse subscription room IDs failed", "account", account, "error", err)
		return nil, natsrouter.ErrInternal("unexpected search response")
	}

	if len(roomIDs) == 0 {
		return &model.SearchSubscriptionsResponse{Subscriptions: []model.SearchSubscription{}}, nil
	}

	subs, err := h.mongo.HydrateSubscriptions(ctx, account, roomIDs)
	if err != nil {
		slog.Error("subscription hydration failed", "account", account, "error", err)
		return nil, natsrouter.ErrInternal("subscription hydration unavailable")
	}

	if subs == nil {
		subs = []model.SearchSubscription{}
	}
	return &model.SearchSubscriptionsResponse{Subscriptions: subs}, nil
}

// loadRestricted implements the 2-tier Valkey → ES read. Cache errors
// alone never fail the request — log-and-fall-through. Only when both
// cache AND ES prefetch fail do we surface ErrInternal.
func (h *handler) loadRestricted(ctx context.Context, account string) (map[string]int64, error) {
	cached, hit, cerr := h.cache.GetRestricted(ctx, account)
	if cerr != nil {
		slog.Warn("valkey read failed; falling through to ES", "account", account, "error", cerr)
	}
	if hit {
		return cached, nil
	}
	doc, _, err := h.store.GetUserRoomDoc(ctx, account)
	if err != nil {
		// Always log the store error, even if the cache GET also failed
		// — it's the actionable signal when both fail. Include cache_err
		// so operators can correlate, but don't let the cache warning
		// mask the ES root cause.
		slog.Error("user-room doc fetch failed", "account", account, "error", err, "cache_err", cerr)
		return nil, natsrouter.ErrInternal("unable to resolve room access")
	}

	restricted := doc.RestrictedRooms
	if restricted == nil {
		// Covers both "user has no subs" (found=false) and "doc exists
		// but has no restricted rooms" — cache an empty map to prevent
		// miss-storms.
		restricted = map[string]int64{}
	}

	// Skip the SET when the GET already errored — the transport is
	// almost certainly still down and a second warning adds noise
	// without new signal.
	if cerr == nil {
		if err := h.cache.SetRestricted(ctx, account, restricted, h.cfg.RestrictedRoomsCacheTTL); err != nil {
			slog.Warn("valkey set failed; continuing without cache", "account", account, "error", err)
		}
	}
	return restricted, nil
}

func (h *handler) searchApps(c *natsrouter.Context, req model.SearchAppsRequest) (resp *model.SearchAppsResponse, err error) {
	defer observeRequest(metricKindApps, &err)()

	account, rerr := c.Params.Require("account")
	if rerr != nil {
		return nil, rerr
	}

	if err := h.normalizePagination(&req.Size, &req.Offset); err != nil {
		return nil, err
	}

	nameQuery := strings.TrimSpace(req.NameQuery)
	if nameQuery == "" {
		return nil, natsrouter.ErrBadRequest("nameQuery is required")
	}

	ctx, cancel := h.withRequestTimeout(c)
	defer cancel()

	apps, err := h.mongo.SearchAppsByName(ctx, nameQuery, account, req.AssistantEnabled, req.Offset, req.Size)
	if err != nil {
		slog.Error("app search backend failed", "account", account, "error", err)
		return nil, natsrouter.ErrInternal("search backend unavailable")
	}

	if apps == nil {
		apps = []model.App{}
	}
	return &model.SearchAppsResponse{Apps: apps}, nil
}

// searchUsers proxies the query to the third-party HR endpoint via
// SearchUsersClient and returns a raw []model.SearchUser. The account
// from the subject is used for logging and metrics only; scoping is
// enforced entirely by the third-party endpoint.
func (h *handler) searchUsers(c *natsrouter.Context, req model.SearchUsersRequest) (resp *[]model.SearchUser, err error) {
	defer observeRequest(metricKindUsers, &err)()

	account, rerr := c.Params.Require("account")
	if rerr != nil {
		return nil, rerr
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, natsrouter.ErrBadRequest("query is required")
	}

	ctx, cancel := h.withRequestTimeout(c)
	defer cancel()

	users, err := h.users.SearchUsers(ctx, query)
	if err != nil {
		slog.Error("user search backend failed", "account", account, "error", err)
		return nil, natsrouter.ErrInternal("user search backend unavailable")
	}

	if users == nil {
		users = []model.SearchUser{}
	}
	return &users, nil
}

// normalizePagination validates and clamps size/offset in place. size=0
// falls back to DocCounts; size>MaxDocCounts is capped. Negative size
// or offset is a client bug, not a defaultable value, so it returns
// ErrBadRequest.
func (h *handler) normalizePagination(size, offset *int) error {
	if *size < 0 || *offset < 0 {
		return natsrouter.ErrBadRequest("size and offset must be non-negative")
	}
	if *size == 0 {
		*size = h.cfg.DocCounts
	}
	if *size > h.cfg.MaxDocCounts {
		*size = h.cfg.MaxDocCounts
	}
	return nil
}
