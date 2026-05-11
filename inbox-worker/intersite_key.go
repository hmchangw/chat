package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/roomkeymetrics"
	"github.com/hmchangw/chat/pkg/subject"
)

// errRoomKeyAbsent fires when the origin RPC reports it has no key for the room
// (Valkey responded but the entry is missing). Distinct from transient RPC
// failures so callers can errors.Is and treat as a permanent miss.
var errRoomKeyAbsent = errors.New("room key absent on origin")

// originErrRoomKeyNotFound is the on-wire string room-worker emits when its own
// errRoomKeyNotFound sentinel propagates through natsutil.ReplyError. Matched
// here to re-attach a sentinel on this side of the RPC boundary.
const originErrRoomKeyNotFound = "room key not found"

// natsInterSiteKeyClient pulls a room's keypair from the origin site via NATS request/reply.
type natsInterSiteKeyClient struct {
	nc      *nats.Conn
	timeout time.Duration
}

func newNatsInterSiteKeyClient(nc *nats.Conn, timeout time.Duration) *natsInterSiteKeyClient {
	return &natsInterSiteKeyClient{nc: nc, timeout: timeout}
}

// GetRoomKey issues chat.server.request.roomkey.{originSiteID}.get and returns the unmarshaled event.
func (c *natsInterSiteKeyClient) GetRoomKey(ctx context.Context, originSiteID, roomID string) (*model.RoomKeyEvent, error) {
	start := time.Now()
	defer func() {
		roomkeymetrics.RPCDuration.Record(ctx, time.Since(start).Seconds())
	}()

	body, err := json.Marshal(model.RoomKeyGetRequest{RoomID: roomID})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	msg := natsutil.NewMsg(rctx, subject.ServerRoomKeyGet(originSiteID), body)
	resp, err := c.nc.RequestMsgWithContext(rctx, msg)
	if err != nil {
		return nil, fmt.Errorf("rpc roomkey get: %w", err)
	}
	if errResp, ok := natsutil.TryParseError(resp.Data); ok {
		if errResp.Error == originErrRoomKeyNotFound {
			return nil, fmt.Errorf("origin: %w", errRoomKeyAbsent)
		}
		return nil, fmt.Errorf("origin error: %s", errResp.Error)
	}
	var evt model.RoomKeyEvent
	if err := json.Unmarshal(resp.Data, &evt); err != nil {
		return nil, fmt.Errorf("unmarshal reply: %w", err)
	}
	return &evt, nil
}
