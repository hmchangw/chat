package main

import (
	"context"
	"encoding/json"
	"fmt"

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
