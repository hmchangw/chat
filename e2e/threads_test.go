//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// getThreadMessagesRequest / Response mirror history-service/internal/models.
// Internal package not importable from e2e/.
type getThreadMessagesRequest struct {
	ThreadMessageID string `json:"threadMessageId"`
	Cursor          string `json:"cursor,omitempty"`
	Limit           int    `json:"limit"`
}

type getThreadMessagesResponse struct {
	Messages   []cassandra.Message `json:"messages"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

// TestThread_ParentAndReplies exercises the MsgThreadPattern handler that
// previously had zero e2e coverage. Flow:
//
//  1. alice sends a parent message in a channel
//  2. wait for ack
//  3. alice sends a reply with ThreadParentMessageID set
//  4. wait for second ack
//  5. query subject.MsgThread for the parent; assert reply is in the response
//
// Thread infrastructure spans separate Cassandra tables
// (thread_messages_by_room) and Mongo collections (thread_rooms,
// thread_subscriptions) -- this test exercises the canonical-path write of
// a reply tagged with ThreadParentMessageID.
func TestThread_ParentAndReplies(t *testing.T) {
	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

	// 1. Channel + subscription wait.
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
	roomID := createReply.RoomID
	require.NotEmpty(t, roomID)
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)

	mongoA := site.MongoDB(t)
	awaitSubscription(t, ctx, mongoA, alice.Account, roomID)
	awaitSubscription(t, ctx, mongoA, bob.Account, roomID)

	js := site.JetStream(t)
	canonical := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-worker")

	// 2. Parent message.
	parentInfo, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	parentPreSeq := parentInfo.CachedInfo().State.LastSeq

	parentID := idgen.GenerateMessageID()
	parentReqID := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		parentReqID,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        parentID,
			Content:   "thread parent " + t.Name(),
			RequestID: parentReqID,
		},
		10*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", parentPreSeq+1)

	// 3. Read parent's CreatedAt from history; the reply needs it as
	// ThreadParentMessageCreatedAt (millisecond epoch).
	var histResp loadHistoryResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgHistory(alice.Account, roomID, site.SiteID),
		loadHistoryRequest{Limit: 50},
		5*time.Second, &histResp,
	))
	var parentCreatedAtMs int64
	for _, m := range histResp.Messages {
		if m.MessageID == parentID {
			parentCreatedAtMs = m.CreatedAt.UnixMilli()
			break
		}
	}
	require.NotZero(t, parentCreatedAtMs, "parent message must be findable in history")

	// 4. Reply tagged with the parent's thread fields.
	replyInfo, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	replyPreSeq := replyInfo.CachedInfo().State.LastSeq

	replyID := idgen.GenerateMessageID()
	replyReqID := idgen.GenerateRequestID()
	replyBody := "thread reply " + t.Name()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		replyReqID,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:                           replyID,
			Content:                      replyBody,
			RequestID:                    replyReqID,
			ThreadParentMessageID:        parentID,
			ThreadParentMessageCreatedAt: &parentCreatedAtMs,
		},
		10*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", replyPreSeq+1)

	// 5. GetThreadMessages should return the reply.
	require.Eventually(t, func() bool {
		var threadResp getThreadMessagesResponse
		err := requestReply(
			alice.Conn(),
			// MsgThreadPattern is the natsrouter handler; the publish-side
			// subject substitutes the placeholders.
			"chat.user."+alice.Account+".request.room."+roomID+"."+site.SiteID+".msg.thread",
			getThreadMessagesRequest{
				ThreadMessageID: parentID,
				Limit:           50,
			},
			5*time.Second, &threadResp,
		)
		if err != nil {
			return false
		}
		for _, m := range threadResp.Messages {
			if m.MessageID == replyID {
				assert.Equal(t, replyBody, m.Msg)
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond,
		"thread reply %s never visible via MsgThread query", replyID)
}
