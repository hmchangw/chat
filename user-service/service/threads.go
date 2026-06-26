package service

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
)

const (
	defaultThreadListLimit = 20
	maxThreadListLimit     = 100
)

// threadCursor is the composite, value-based pagination position on the global
// (lastMsgAt DESC, threadRoomId DESC) sort key. It is sent verbatim to every
// site each page, so pagination is stateless and per-site-failure tolerant.
type threadCursor struct {
	LastMsgAt    int64  `json:"lastMsgAt"`
	ThreadRoomID string `json:"threadRoomId"`
}

func encodeThreadCursor(c threadCursor) string {
	b, _ := json.Marshal(c) // marshaling two scalars never fails
	return base64.StdEncoding.EncodeToString(b)
}

func decodeThreadCursor(s string) (*threadCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errcode.BadRequest("invalid cursor")
	}
	var c threadCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, errcode.BadRequest("invalid cursor")
	}
	return &c, nil
}

// ListUserThreads is the cross-site thread inbox: it gathers each federation
// site's thread list for the user (local site directly, remote sites fanned
// out) and merges the results into one globally-ordered page with a composite
// cursor.
// NATS: chat.user.{account}.request.user.{siteID}.thread.list
func (s *UserService) ListUserThreads(c *natsrouter.Context, req model.ThreadListRequest) (*model.ThreadListResponse, error) {
	account := c.Param("account")
	c.WithLogValues("account", account)

	limit := req.Limit
	if limit <= 0 {
		limit = defaultThreadListLimit
	}
	if limit > maxThreadListLimit {
		limit = maxThreadListLimit
	}

	cursor, err := decodeThreadCursor(req.Cursor)
	if err != nil {
		return nil, err
	}

	sites := s.threadFanoutSites()
	if len(sites) == 0 {
		return &model.ThreadListResponse{Items: []model.ThreadListItem{}, HasNext: false}, nil
	}

	leafReq := model.ThreadSubscriptionListRequest{Account: account, Limit: limit}
	if cursor != nil {
		leafReq.CursorLastMsgAt = &cursor.LastMsgAt
		leafReq.CursorThreadRoomID = cursor.ThreadRoomID
	}

	results := s.getThreadLists(c, sites, leafReq)

	var unavailable []string
	anyHasMore := false
	merged := make([]model.ThreadListItem, 0, limit*len(sites))
	for i, site := range sites {
		if results[i].failed {
			unavailable = append(unavailable, site)
			continue
		}
		anyHasMore = anyHasMore || results[i].hasMore
		merged = append(merged, results[i].items...)
	}

	// Global order: newest activity first, threadRoomId as the stable tiebreak.
	sort.Slice(merged, func(a, b int) bool {
		if merged[a].LastMsgAt != merged[b].LastMsgAt {
			return merged[a].LastMsgAt > merged[b].LastMsgAt
		}
		return merged[a].ThreadRoomID > merged[b].ThreadRoomID
	})
	merged = dedupeThreads(merged)

	hasNext := anyHasMore || len(merged) > limit
	if len(merged) > limit {
		merged = merged[:limit]
	}

	resp := &model.ThreadListResponse{Items: merged, HasNext: hasNext, UnavailableSites: unavailable}
	if hasNext && len(merged) > 0 {
		last := merged[len(merged)-1]
		resp.NextCursor = encodeThreadCursor(threadCursor{LastMsgAt: last.LastMsgAt, ThreadRoomID: last.ThreadRoomID})
	}
	return resp, nil
}

type threadSiteResult struct {
	items   []model.ThreadListItem
	hasMore bool
	failed  bool
}

// threadFanoutSites returns the sites to query for the inbox: the caller's own
// site (always, served directly) plus every configured federation site, deduped
// and non-blank. The home site can't reliably enumerate which sites hold the
// user's threads from local subscriptions, so we ask every site — each
// history-service answers for its own threads (empty if none). Mirrors how
// publishStatus fans out over ALL_SITE_IDS.
func (s *UserService) threadFanoutSites() []string {
	seen := make(map[string]struct{}, len(s.allSiteIDs)+1)
	sites := make([]string, 0, len(s.allSiteIDs)+1)
	add := func(id string) {
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		sites = append(sites, id)
	}
	add(s.siteID)
	for _, id := range s.allSiteIDs {
		add(id)
	}
	return sites
}

// getThreadLists fetches each candidate site's thread list, mirroring
// enrichWithRoomInfo: the caller's own site is served directly (enrichLocalThreads,
// no fan-out machinery), while remote sites fan out concurrently
// (enrichCrossSiteThreads). Results are indexed by site position.
func (s *UserService) getThreadLists(c *natsrouter.Context, sites []string, leafReq model.ThreadSubscriptionListRequest) []threadSiteResult {
	results := make([]threadSiteResult, len(sites))
	var localIdx, crossIdx []int
	for i, site := range sites {
		if site == s.siteID {
			localIdx = append(localIdx, i)
		} else {
			crossIdx = append(crossIdx, i)
		}
	}
	s.enrichLocalThreads(c, sites, results, localIdx, leafReq)
	s.enrichCrossSiteThreads(c, sites, results, crossIdx, leafReq)
	return results
}

// enrichLocalThreads serves the caller's own site with a single direct
// GetThreadList call — no goroutine/semaphore. (user-service shares the local
// MongoDB but not Cassandra, so message hydration still goes through the local
// history-service; the saving over the cross-site path is the fan-out machinery,
// not the RPC itself.)
func (s *UserService) enrichLocalThreads(c *natsrouter.Context, sites []string, results []threadSiteResult, localIdx []int, leafReq model.ThreadSubscriptionListRequest) {
	for _, i := range localIdx {
		resp, err := s.history.GetThreadList(c, sites[i], leafReq)
		if err != nil {
			slog.WarnContext(c, "thread-list local degraded", "account", leafReq.Account, "site", sites[i], "request_id", natsutil.RequestIDFromContext(c), "error", err)
			results[i].failed = true
			continue
		}
		results[i] = threadSiteResult{items: resp.Items, hasMore: resp.HasMore}
	}
}

// enrichCrossSiteThreads issues the per-site GetThreadList RPC for remote sites
// concurrently (bounded by maxSiteFanout). A failed site is marked failed and
// degrades gracefully — siblings keep running (no errgroup cancellation).
func (s *UserService) enrichCrossSiteThreads(c *natsrouter.Context, sites []string, results []threadSiteResult, crossIdx []int, leafReq model.ThreadSubscriptionListRequest) {
	if len(crossIdx) == 0 {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxSiteFanout)
	for _, i := range crossIdx {
		if c.Err() != nil {
			results[i].failed = true
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if c.Err() != nil {
				results[i].failed = true
				return
			}
			resp, err := s.history.GetThreadList(c, sites[i], leafReq)
			if err != nil {
				slog.WarnContext(c, "thread-list site degraded", "account", leafReq.Account, "site", sites[i], "request_id", natsutil.RequestIDFromContext(c), "error", err)
				results[i].failed = true
				return
			}
			results[i] = threadSiteResult{items: resp.Items, hasMore: resp.HasMore}
		}()
	}
	wg.Wait()
}

// dedupeThreads drops repeat threadRoomIds, keeping the first (newest) — guards
// against the deletion edge where a recomputed lastMsgAt could resurface an
// already-emitted thread on a later page.
func dedupeThreads(items []model.ThreadListItem) []model.ThreadListItem {
	seen := make(map[string]struct{}, len(items))
	out := items[:0:0]
	for i := range items {
		if _, dup := seen[items[i].ThreadRoomID]; dup {
			continue
		}
		seen[items[i].ThreadRoomID] = struct{}{}
		out = append(out, items[i])
	}
	return out
}
