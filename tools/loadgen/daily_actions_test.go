package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

type captured struct {
	mu   sync.Mutex
	pubs []capturedPub
	reqs []capturedReq
}
type capturedPub struct {
	Subj string
	Data []byte
}
type capturedReq struct {
	Subj string
	Data []byte
}

func (c *captured) publish(_ context.Context, subj string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pubs = append(c.pubs, capturedPub{Subj: subj, Data: append([]byte(nil), data...)})
	return nil
}
func (c *captured) request(_ context.Context, subj string, data []byte, _ time.Duration) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, capturedReq{Subj: subj, Data: append([]byte(nil), data...)})
	return []byte(`{"ok":true}`), nil
}

func TestSendMessage_PublishesToFrontdoor(t *testing.T) {
	c := &captured{}
	u := &userState{ID: "u-1", Account: "user-1", Rooms: []string{"room-a", "room-b"}}
	ctx := actionCtx{Ctx: context.Background(), Publish: c.publish, Request: c.request, SiteID: "site-test"}
	err := sendMessage(ctx, u, "hello")
	require.NoError(t, err)
	require.Len(t, c.pubs, 1)
	got := c.pubs[0]
	require.True(t, got.Subj == subject.MsgSend("user-1", "room-a", "site-test") ||
		got.Subj == subject.MsgSend("user-1", "room-b", "site-test"))
	var req model.SendMessageRequest
	require.NoError(t, json.Unmarshal(got.Data, &req))
	require.Equal(t, "hello", req.Content)
}

func TestReadReceipt_Publishes(t *testing.T) {
	c := &captured{}
	u := &userState{ID: "u-1", Account: "user-1", Rooms: []string{"room-a"}}
	ctx := actionCtx{Ctx: context.Background(), Publish: c.publish, Request: c.request, SiteID: "site-test"}
	err := readReceipt(ctx, u, "msg-1")
	require.NoError(t, err)
	require.Len(t, c.pubs, 1)
	require.Equal(t, subject.MessageRead("user-1", "room-a", "site-test"), c.pubs[0].Subj)
}

func TestRefreshRoomList_Requests(t *testing.T) {
	c := &captured{}
	u := &userState{ID: "u-1", Account: "user-1"}
	ctx := actionCtx{Ctx: context.Background(), Publish: c.publish, Request: c.request, SiteID: "site-test"}
	err := refreshRoomList(ctx, u)
	require.NoError(t, err)
	require.Len(t, c.reqs, 1)
	require.Equal(t, subject.UserSubscriptionGetRooms("user-1", "site-test"), c.reqs[0].Subj)
}
