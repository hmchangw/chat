package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// publishFn matches the existing Publisher interface used by generator.go.
type publishFn func(ctx context.Context, subj string, data []byte) error

// requestFn does a NATS request/reply.
type requestFn func(ctx context.Context, subj string, data []byte, timeout time.Duration) ([]byte, error)

// actionCtx bundles everything every action handler needs. Keeps function
// signatures small and tests easy to write.
type actionCtx struct {
	Ctx       context.Context
	Publish   publishFn
	Request   requestFn
	SiteID    string
	Collector *Collector // optional; for latency correlation
	Rand      *rand.Rand // optional; falls back to a per-call source
}

func (a actionCtx) rand() *rand.Rand {
	if a.Rand != nil {
		return a.Rand
	}
	return rand.New(rand.NewSource(time.Now().UnixNano()))
}

const defaultRequestTimeout = 5 * time.Second

// sendMessage publishes a SendMessageRequest on the frontdoor subject for a
// random room the user belongs to. If u has no rooms, returns nil (noop).
func sendMessage(a actionCtx, u *userState, content string) error {
	if len(u.Rooms) == 0 {
		return nil
	}
	roomID := u.Rooms[a.rand().Intn(len(u.Rooms))]
	msgID := idgen.GenerateMessageID()
	reqID := idgen.GenerateRequestID()
	req := model.SendMessageRequest{ID: msgID, Content: content, RequestID: reqID}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal send-message: %w", err)
	}
	if a.Collector != nil {
		a.Collector.RecordPublish(reqID, msgID, time.Now())
	}
	if err := a.Publish(a.Ctx, subject.MsgSend(u.Account, roomID, a.SiteID), data); err != nil {
		if a.Collector != nil {
			a.Collector.RecordPublishFailed(reqID, msgID)
		}
		return fmt.Errorf("publish send-message: %w", err)
	}
	return nil
}

// readReceipt publishes a read-receipt event for a random room.
func readReceipt(a actionCtx, u *userState, lastMsgID string) error {
	if len(u.Rooms) == 0 {
		return nil
	}
	roomID := u.Rooms[a.rand().Intn(len(u.Rooms))]
	payload, err := json.Marshal(map[string]string{"messageId": lastMsgID})
	if err != nil {
		return fmt.Errorf("marshal read-receipt: %w", err)
	}
	if err := a.Publish(a.Ctx, subject.MessageRead(u.Account, roomID, a.SiteID), payload); err != nil {
		return fmt.Errorf("publish read-receipt: %w", err)
	}
	return nil
}

// refreshRoomList does a NATS request/reply for the user's subscription list.
func refreshRoomList(a actionCtx, u *userState) error {
	_, err := a.Request(a.Ctx, subject.UserSubscriptionGetRooms(u.Account, a.SiteID), nil, defaultRequestTimeout)
	if err != nil {
		return fmt.Errorf("request room-list: %w", err)
	}
	return nil
}
