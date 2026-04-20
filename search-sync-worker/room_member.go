package main

import (
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// roomMemberCollection is the shared base for collections that index
// subscription lifecycle events (member_added, member_removed) from the
// ROOMS stream. Room-worker publishes enriched MemberAddEvent /
// MemberRemoveEvent to `chat.room.canonical.{site}.member_added/removed`
// which lands in the ROOMS_{siteID} stream. For cross-site events,
// inbox-worker re-publishes to the local ROOMS stream after creating
// subscriptions.
type roomMemberCollection struct{}

func (b *roomMemberCollection) StreamConfig(siteID string) jetstream.StreamConfig {
	c := stream.Rooms(siteID)
	return jetstream.StreamConfig{Name: c.Name, Subjects: c.Subjects}
}

func (b *roomMemberCollection) FilterSubjects(siteID string) []string {
	return subject.RoomCanonicalMemberEventSubjects(siteID)
}

// memberEvent is a tagged union returned by parseMemberEvent. Exactly one
// of Add or Remove is non-nil.
type memberEvent struct {
	Add    *model.MemberAddEvent
	Remove *model.MemberRemoveEvent
}

// parseMemberEvent peeks at the `type` field in the raw JSON, then
// unmarshals into the appropriate event struct. Returns an error for
// unknown types so mispublished messages don't silently reach the wrong
// indexing path.
func parseMemberEvent(data []byte) (*memberEvent, error) {
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return nil, fmt.Errorf("unmarshal member event type: %w", err)
	}

	switch peek.Type {
	case "member_added":
		var evt model.MemberAddEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil, fmt.Errorf("unmarshal member_added event: %w", err)
		}
		if evt.Timestamp <= 0 {
			return nil, fmt.Errorf("parse member_added event: missing timestamp")
		}
		if evt.RoomID == "" {
			return nil, fmt.Errorf("parse member_added event: missing roomID")
		}
		if len(evt.Accounts) == 0 {
			return nil, fmt.Errorf("parse member_added event: empty accounts")
		}
		for i, account := range evt.Accounts {
			if account == "" {
				return nil, fmt.Errorf("parse member_added event: empty account at index %d", i)
			}
		}
		return &memberEvent{Add: &evt}, nil

	case "member_removed", "member_left":
		var evt model.MemberRemoveEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil, fmt.Errorf("unmarshal member_removed event: %w", err)
		}
		if evt.Timestamp <= 0 {
			return nil, fmt.Errorf("parse member_removed event: missing timestamp")
		}
		if evt.RoomID == "" {
			return nil, fmt.Errorf("parse member_removed event: missing roomID")
		}
		if len(evt.Accounts) == 0 {
			return nil, fmt.Errorf("parse member_removed event: empty accounts")
		}
		for i, account := range evt.Accounts {
			if account == "" {
				return nil, fmt.Errorf("parse member_removed event: empty account at index %d", i)
			}
		}
		return &memberEvent{Remove: &evt}, nil

	default:
		return nil, fmt.Errorf("parse member event: unsupported type %q", peek.Type)
	}
}
