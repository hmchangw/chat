//go:build e2e

package scenarios

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/e2e"
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// searchMessagesRequest mirrors search-service's request payload (defined
// inside the search-service package, not exported as a model type).
type searchMessagesRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// searchHit is the minimum we assert on; the actual reply carries richer
// fields (score, highlights, etc.) which we don't pin here.
type searchHit struct {
	ID      string `json:"id"`
	Content string `json:"content,omitempty"`
}

type searchMessagesResponse struct {
	Hits []searchHit `json:"hits"`
}

// TestSearch_MessageVisibleAfterIndex: send a message with two unique tokens,
// then poll search-service until the index catches up. Per amendment R2.C
// item 4: assert the exact message ID matches the one alice sent -- turns a
// smoke "any hit" assertion into a real correctness check.
//
// The 200ms refresh_interval (set by search-init-a per R1 4.B) keeps the
// poll window small. 10s is generous; typical observed catch-up is under 1s.
func TestSearch_MessageVisibleAfterIndex(t *testing.T) {
	ctx := t.Context()
	site := e2e.Stack().SiteA

	alice := site.Authenticate(t, ctx, "alice")
	_ = site.Authenticate(t, ctx, "bob")

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

	msgID := idgen.GenerateMessageID()
	body := "pumpernickel xylophone " + t.Name()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		idgen.GenerateRequestID(),
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   body,
			RequestID: idgen.GenerateRequestID(),
		},
		10*time.Second,
	))

	// Poll search-service until the message is indexed. Subject is
	// subject.SearchMessages(account) per R1 11.A.
	deadline := time.Now().Add(10 * time.Second)
	var resp searchMessagesResponse
	for time.Now().Before(deadline) {
		resp = searchMessagesResponse{}
		err := requestReply(
			alice.Conn(),
			subject.SearchMessages(alice.Account),
			searchMessagesRequest{Query: "pumpernickel", Limit: 25},
			3*time.Second,
			&resp,
		)
		if err == nil && len(resp.Hits) > 0 {
			break
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("ctx canceled waiting for search hit")
		}
	}
	require.NotEmpty(t, resp.Hits, "search-sync-worker did not index the message within 10s")

	// Per amendment R2.C item 4: assert exact message ID match, not just "any hit".
	var foundIDs []string
	for _, h := range resp.Hits {
		foundIDs = append(foundIDs, h.ID)
	}
	assert.Contains(t, foundIDs, msgID,
		"search must return the exact message ID for the unique query; got hits=%s",
		mustJSON(t, resp.Hits))
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
