//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/e2e/harness"
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// editMessageRequest / editMessageResponse / deleteMessageRequest /
// deleteMessageResponse mirror history-service/internal/models -- that package
// is internal and not importable from e2e/. JSON tags pinned to the wire form.
type editMessageRequest struct {
	MessageID string `json:"messageId"`
	NewMsg    string `json:"newMsg"`
}

type editMessageResponse struct {
	MessageID string `json:"messageId"`
	EditedAt  int64  `json:"editedAt"`
}

type deleteMessageRequest struct {
	MessageID string `json:"messageId"`
}

type deleteMessageResponse struct {
	MessageID string `json:"messageId"`
	DeletedAt int64  `json:"deletedAt"`
}

// setupSingleSiteRoomWithMessage: alice creates a channel with bob, sends
// one message. Returns the roomID + msgID + sent body for assertions.
// Shared fixture for the edit + delete tests.
func setupSingleSiteRoomWithMessage(t *testing.T) (roomID, msgID, body string, alice, bob *harness.Identity) {
	t.Helper()
	ctx := t.Context()
	site := stack.SiteA

	alice = site.Authenticate(t, ctx, "alice")
	bob = site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID = createReply.RoomID
	require.NotEmpty(t, roomID)
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)

	mongoA := site.MongoDB(t)
	awaitSubscription(t, ctx, mongoA, alice.Account, roomID)
	awaitSubscription(t, ctx, mongoA, bob.Account, roomID)

	// Snapshot canonical seq so we can wait for message-worker to ack our
	// send before edit/delete reach history-service (otherwise history sees
	// no message and rejects with "message not found").
	ctx2 := t.Context()
	js := site.JetStream(t)
	canonicalStream := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx2, js, canonicalStream, "message-worker")
	preInfo, err := js.Stream(ctx2, canonicalStream)
	require.NoError(t, err)
	preSeq := preInfo.CachedInfo().State.LastSeq

	msgID = idgen.GenerateMessageID()
	reqID := idgen.GenerateRequestID()
	body = "original message " + t.Name()
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

	// Wait for the canonical ack so the message exists in Cassandra before
	// subsequent edit/delete RPCs hit history-service.
	awaitCanonicalAcked(t, ctx2, js, canonicalStream, "message-worker", preSeq+1)

	return roomID, msgID, body, alice, bob
}

// TestMessage_Edit exercises history-service's MsgEditPattern handler --
// previously zero e2e coverage. alice sends, alice edits, LoadHistory
// returns the edited content.
func TestMessage_Edit(t *testing.T) {
	t.Parallel()
	site := stack.SiteA
	roomID, msgID, _, alice, _ := setupSingleSiteRoomWithMessage(t)

	newMsg := "edited content " + t.Name()
	var editResp editMessageResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgEdit(alice.Account, roomID, site.SiteID),
		editMessageRequest{MessageID: msgID, NewMsg: newMsg},
		5*time.Second, &editResp,
	))
	assert.Equal(t, msgID, editResp.MessageID)
	assert.Positive(t, editResp.EditedAt, "EditedAt must be non-zero")

	require.Eventually(t, func() bool {
		var histResp loadHistoryResponse
		err := requestReply(
			alice.Conn(),
			subject.MsgHistory(alice.Account, roomID, site.SiteID),
			loadHistoryRequest{Limit: 50},
			5*time.Second, &histResp,
		)
		if err != nil {
			return false
		}
		for _, m := range histResp.Messages {
			if m.MessageID == msgID {
				return m.Msg == newMsg
			}
		}
		return false
	}, 10*time.Second, 250*time.Millisecond,
		"history must reflect edited message content")
}

// TestMessage_Delete exercises history-service's MsgDeletePattern handler.
// alice sends, alice deletes, the message is filtered from subsequent
// LoadHistory responses (Cassandra soft-delete on the message row).
func TestMessage_Delete(t *testing.T) {
	t.Parallel()
	site := stack.SiteA
	roomID, msgID, _, alice, _ := setupSingleSiteRoomWithMessage(t)

	var delResp deleteMessageResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgDelete(alice.Account, roomID, site.SiteID),
		deleteMessageRequest{MessageID: msgID},
		5*time.Second, &delResp,
	))
	assert.Equal(t, msgID, delResp.MessageID)
	assert.Positive(t, delResp.DeletedAt)

	// Soft-delete sets `deleted = true` on the Cassandra row (see
	// cassrepo/write.go: deleteMsgByIDCAS). LoadHistory may either filter
	// deleted rows or return them with the Deleted flag set; either way is
	// a correct contract. Accept both: pass if the message is absent OR
	// present-and-flagged.
	require.Eventually(t, func() bool {
		var histResp loadHistoryResponse
		err := requestReply(
			alice.Conn(),
			subject.MsgHistory(alice.Account, roomID, site.SiteID),
			loadHistoryRequest{Limit: 50},
			5*time.Second, &histResp,
		)
		if err != nil {
			return false
		}
		for _, m := range histResp.Messages {
			if m.MessageID == msgID {
				return m.Deleted // present but marked deleted
			}
		}
		return true // absent
	}, 10*time.Second, 250*time.Millisecond,
		"LoadHistory must filter or flag the deleted message")
}
