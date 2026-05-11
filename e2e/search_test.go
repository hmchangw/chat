//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// Search request/response use the canonical pkg/model types
// (SearchMessagesRequest{SearchText,Size,...}, SearchMessagesResponse
// {Total,Results}). The original chapter-spec hand-rolled mirror was wrong.

// TestSearch_MessageVisibleAfterIndex: send a message with two unique tokens,
// then poll search-service until the index catches up. Per amendment R2.C
// item 4: assert the exact message ID matches the one alice sent -- turns a
// smoke "any hit" assertion into a real correctness check.
//
// The 200ms refresh_interval (set by search-init-a per R1 4.B) keeps the
// poll window small. 10s is generous; typical observed catch-up is under 1s.
func TestSearch_MessageVisibleAfterIndex(t *testing.T) {
	// LIVE-RUN FINDING (post cheap-#3 SeedUserRoom attempt):
	//
	// search-service has THREE layers of authorization caching that
	// independently need to be coherent for a search to return results:
	//
	//   1. Mongo-side restricted-rooms (per room.HistorySharedSince).
	//   2. Valkey-cached restricted-rooms set (TTL 5 min, key
	//      `searchservice:restrictedrooms:{account}`).
	//   3. ES user-room doc (per-user; populated by search-sync-worker from
	//      INBOX events; we seed it directly via SeedUserRoom).
	//
	// Even after pre-seeding (3), flushing (2), and restarting
	// search-service-a to drop its in-process cache, the search query
	// returns 0 results. The user-room doc has alice + the test roomID
	// correctly; the messages index has alice's pumpernickel message with
	// the same roomID; but the final search returns empty.
	//
	// The remaining hypothesis is search-service's query builder
	// (`buildMessageQuery`) combines the user-room rooms[] list with
	// restricted-rooms hss-window filtering and the roomTimestamps
	// last-write-wins guard in a way the e2e harness can't yet replicate
	// without a deeper seed. Unblocking this needs a follow-up plan that
	// either (a) writes a fuller user-room doc including roomTimestamps
	// for system messages too, or (b) drops the 5-minute valkey TTL to a
	// test-friendly window via env override, or (c) authenticates alice
	// on siteB so cross-site sync populates user-room properly.
	// Unblock attempt (per R3): the missing piece in earlier runs was that
	// search-service's per-account valkey cache (key
	// `searchservice:restrictedrooms:{account}`, TTL 5m) can be populated
	// from a prior test with an empty/wrong rooms set. The terms-lookup uses
	// the user-room ES doc (which we DO seed), but the cache short-circuits
	// the doc fetch when warm. Flushing the cache for alice forces
	// search-service to re-read the doc we just seeded.

	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	_ = site.Authenticate(t, ctx, "bob")

	site.FlushSearchRestrictedCache(t, alice.Account)

	// Set up a channel + send a message with two unique two-token strings.
	// `pumpernickel xylophone` won't appear in any other test fixture and
	// is highly unlikely to be a false-positive in other suites.
	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{"bob"},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)

	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)

	// Pre-seed alice's user-room ES doc with this roomID so search-service
	// authorizes her query. See SeedUserRoom for why this is needed.
	site.SeedUserRoom(t, alice.Account, []string{roomID})

	// Same RequestID for both the request body and the response-subject
	// suffix; gatekeeper derives the reply subject from the body's
	// RequestID, so they must agree.
	js := site.JetStream(t)
	canonical := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-sync")
	preInfo, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	preSeq := preInfo.CachedInfo().State.LastSeq

	msgID := idgen.GenerateMessageID()
	body := "pumpernickel xylophone " + t.Name()
	reqID := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   body,
			RequestID: reqID,
		},
		10*time.Second,
	))
	// Wait for search-sync-worker to ack the canonical event (it's the one
	// that writes the messages-*-YYYY-MM ES doc; without this we may poll
	// search-service before indexing has started).
	awaitCanonicalAcked(t, ctx, js, canonical, "message-sync", preSeq+1)

	// Poll search-service until the message is indexed. Subject is
	// subject.SearchMessages(account) per R1 11.A.
	deadline := time.Now().Add(15 * time.Second)
	var resp model.SearchMessagesResponse
	for time.Now().Before(deadline) {
		resp = model.SearchMessagesResponse{}
		err := requestReply(
			alice.Conn(),
			subject.SearchMessages(alice.Account),
			model.SearchMessagesRequest{SearchText: "pumpernickel", Size: 25},
			3*time.Second,
			&resp,
		)
		if err == nil && len(resp.Results) > 0 {
			break
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("ctx canceled waiting for search hit")
		}
	}
	require.NotEmpty(t, resp.Results, "search-sync-worker did not index the message within 15s")

	// Per amendment R2.C item 4: assert exact message ID match, not just "any hit".
	var foundIDs []string
	for _, h := range resp.Results {
		foundIDs = append(foundIDs, h.MessageID)
	}
	assert.Contains(t, foundIDs, msgID,
		"search must return the exact message ID for the unique query; got hits=%s",
		mustJSON(t, resp.Results))
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
