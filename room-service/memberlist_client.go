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

	// Always bound remote calls — use the configured timeout, or fall back to 5s
	// (matches envDefault) so a zero/negative misconfiguration can't leak indefinite blocks.
	effectiveTimeout := c.timeout
	if effectiveTimeout <= 0 {
		effectiveTimeout = 5 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, effectiveTimeout)
	defer cancel()

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
		// Map remote "not a member" to the local sentinel so callers can use
		// errors.Is(err, errNotChannelMember) uniformly regardless of which site
		// the source channel lives on. Other remote errors are passed through
		// verbatim via the "remote member.list:" prefix that sanitizeError whitelists.
		if errResp.Error == errNotRoomMember.Error() {
			return nil, errNotChannelMember
		}
		return nil, fmt.Errorf("remote member.list: %s", errResp.Error)
	}

	var resp model.ListRoomMembersResponse
	if err := json.Unmarshal(reply.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal member.list reply: %w", err)
	}
	return resp.Members, nil
}
