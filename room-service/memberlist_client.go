//go:generate mockgen -source=memberlist_client.go -destination=mock_memberlist_client_test.go -package=main

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// MemberListClient fetches room members from a remote site's member.list endpoint.
type MemberListClient interface {
	ListMembers(ctx context.Context, requester string, ch model.ChannelRef) ([]model.RoomMember, error)
}

type natsMemberListClient struct {
	nc      *nats.Conn
	timeout time.Duration
}

// NewNATSMemberListClient creates a NATS-backed MemberListClient.
func NewNATSMemberListClient(nc *nats.Conn, timeout time.Duration) MemberListClient {
	return &natsMemberListClient{nc: nc, timeout: timeout}
}

func (c *natsMemberListClient) ListMembers(ctx context.Context, requester string, ch model.ChannelRef) ([]model.RoomMember, error) {
	body, err := json.Marshal(model.ListRoomMembersRequest{})
	if err != nil {
		return nil, fmt.Errorf("marshal member.list body: %w", err)
	}

	// Zero-timeout misconfiguration would make context.WithTimeout expire immediately; fall through to the caller's ctx.
	reqCtx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	out := &nats.Msg{
		Subject: subject.MemberList(requester, ch.RoomID, ch.SiteID),
		Data:    body,
		Header:  nats.Header{},
	}
	reply, err := c.nc.RequestMsgWithContext(reqCtx, out)
	if err != nil {
		return nil, fmt.Errorf("member.list request to %s: %w", ch.SiteID, err)
	}

	if errResp, ok := natsutil.TryParseError(reply.Data); ok {
		return nil, fmt.Errorf("remote member.list: %s", errResp.Error)
	}

	var resp model.ListRoomMembersResponse
	if err := json.Unmarshal(reply.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal member.list reply: %w", err)
	}
	return resp.Members, nil
}
