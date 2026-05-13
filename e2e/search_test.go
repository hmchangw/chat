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
//
// Parallelism: each invocation mints a fresh Keycloak user via
// MintEphemeralUser so the per-account valkey cache + ES user-room doc
// don't collide with sibling parallel tests. Previously serial because
// it mutated alice's shared state.
func TestSearch_MessageVisibleAfterIndex(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	// Ephemeral user: avoids the shared-alice state (per-account
	// valkey restricted-rooms cache + ES user-room doc) that forced
	// this test to run serial.
	senderName := site.MintEphemeralUser(t, ctx)
	sender := site.Authenticate(t, ctx, senderName)
	otherName := site.MintEphemeralUser(t, ctx)
	other := site.Authenticate(t, ctx, otherName)

	// Flush the cache for sender (defensive -- a fresh user shouldn't
	// have a cache entry, but a prior failed run leaving stale state
	// would be silent otherwise).
	site.FlushSearchRestrictedCache(t, sender.Account)

	// Set up a channel + send a message with two unique two-token strings.
	// `pumpernickel xylophone` won't appear in any other test fixture and
	// is highly unlikely to be a false-positive in other suites.
	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{other.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		sender.Conn(),
		subject.RoomCreate(sender.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, site)}, roomID)

	awaitSubscription(t, ctx, site.MongoDB(t), sender.Account, roomID)

	// Pre-seed sender's user-room ES doc with this roomID so search-service
	// authorizes her query. See SeedUserRoom for why this is needed.
	site.SeedUserRoom(t, sender.Account, []string{roomID})

	// Same RequestID for both the request body and the response-subject
	// suffix; gatekeeper derives the reply subject from the body's
	// RequestID, so they must agree.
	js := site.JetStream(t)
	awaitDurableReady(t, ctx, js, stream.MessagesCanonical(site.SiteID).Name, "message-sync")

	msgID := idgen.GenerateMessageID()
	body := "pumpernickel xylophone " + t.Name()
	reqID := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		sender.Conn(),
		sender.Account,
		reqID,
		subject.MsgSend(sender.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   body,
			RequestID: reqID,
		},
		10*time.Second,
	))
	// Wait for the message to land in Cassandra (parallel-safe by msgID).
	// search-sync-worker writes ES asynchronously after the canonical
	// event lands; the subsequent search-service polling loop tolerates
	// the ES refresh window.
	awaitMessageOnSite(t, ctx, site, msgID)

	// Poll search-service until the message is indexed. Subject is
	// subject.SearchMessages(account) per R1 11.A. Each parallel test
	// uses a UNIQUE sender + body string, so the search-by-substring is
	// safe to dispatch on the body keyword without cross-test collisions.
	deadline := time.Now().Add(30 * time.Second)
	var resp model.SearchMessagesResponse
	for time.Now().Before(deadline) {
		resp = model.SearchMessagesResponse{}
		err := requestReply(
			sender.Conn(),
			subject.SearchMessages(sender.Account),
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
