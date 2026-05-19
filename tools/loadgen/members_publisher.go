package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// MemberPublisher publishes one add-member operation onto NATS. corrID is the
// correlation ID the collector keys reply / broadcast samples against.
type MemberPublisher interface {
	Publish(ctx context.Context, requesterAccount, roomID string,
		req *model.AddMembersRequest, corrID string) error
}

type canonicalMemberPublisher struct {
	js     jetstream.JetStream
	siteID string
}

func newCanonicalMemberPublisher(js jetstream.JetStream, siteID string) *canonicalMemberPublisher {
	return &canonicalMemberPublisher{js: js, siteID: siteID}
}

func (p *canonicalMemberPublisher) Publish(ctx context.Context, _ string, _ string,
	req *model.AddMembersRequest, _ string) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal add-members canonical event: %w", err)
	}
	if _, err := p.js.Publish(ctx, subject.RoomCanonical(p.siteID, "member.add"), data); err != nil {
		return fmt.Errorf("jetstream publish: %w", err)
	}
	return nil
}

// ReplyHandler is called once per inbound frontdoor reply. corrID is the
// correlation token the publisher embedded in the per-request reply inbox.
type ReplyHandler func(corrID string, body []byte, at time.Time)

type frontdoorMemberPublisher struct {
	nc          *nats.Conn
	siteID      string
	inboxPrefix string
	onReply     ReplyHandler
	sub         *nats.Subscription
}

func newFrontdoorMemberPublisher(nc *nats.Conn, siteID string, onReply ReplyHandler) (*frontdoorMemberPublisher, error) {
	// Per-publisher inbox so concurrent loadgen instances don't cross-talk.
	inboxPrefix := "_INBOX.loadgen.members." + nats.NewInbox()[len("_INBOX."):]
	p := &frontdoorMemberPublisher{
		nc:          nc,
		siteID:      siteID,
		inboxPrefix: inboxPrefix,
		onReply:     onReply,
	}
	sub, err := nc.Subscribe(inboxPrefix+".*", func(m *nats.Msg) {
		corr := m.Subject[len(inboxPrefix)+1:]
		p.onReply(corr, m.Data, time.Now())
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe reply inbox: %w", err)
	}
	p.sub = sub
	return p, nil
}

// Close tears down the reply subscription. Safe to call multiple times.
func (p *frontdoorMemberPublisher) Close() {
	if p.sub != nil {
		_ = p.sub.Unsubscribe()
		p.sub = nil
	}
}

func (p *frontdoorMemberPublisher) Publish(_ context.Context, requesterAccount, roomID string,
	req *model.AddMembersRequest, corrID string) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal add-members request: %w", err)
	}
	subj := subject.MemberAdd(requesterAccount, roomID, p.siteID)
	reply := p.inboxPrefix + "." + corrID
	if err := p.nc.PublishRequest(subj, reply, data); err != nil {
		return fmt.Errorf("nats publish request: %w", err)
	}
	return nil
}
