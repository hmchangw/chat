package main

import (
	"encoding/json"
	"fmt"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// roomRequestArgs carries the per-tick inputs the room-rpc scenario uses
// to build a request. Fields are consulted only by the kinds that need
// them; e.g. MemberAccount is only used by MemberAdd.
type roomRequestArgs struct {
	User          model.User
	Room          model.Room
	SiteID        string
	WriteIDPrefix string // forensic-identification prefix on generated IDs/names
	MemberAccount string // candidate user account for MemberAdd
}

// buildRoomRequest builds a NATS request payload + subject for the given
// room-service request kind. Subjects come from pkg/subject builders
// already shared with other consumers; payload structs are the public
// pkg/model request types.
func buildRoomRequest(kind roomRequestKind, args *roomRequestArgs) (string, []byte, error) {
	account := args.User.Account
	roomID := args.Room.ID
	siteID := args.SiteID
	if siteID == "" {
		siteID = args.Room.SiteID
	}

	switch kind {
	case RoomsListKind:
		// rooms.list takes no body — empty JSON object.
		return subject.RoomsList(account), []byte(`{}`), nil

	case RoomsGetKind:
		return subject.RoomsGet(account, roomID), []byte(`{}`), nil

	case MemberListKind:
		body, err := json.Marshal(model.ListRoomMembersRequest{})
		if err != nil {
			return "", nil, fmt.Errorf("marshal ListRoomMembersRequest: %w", err)
		}
		return subject.MemberList(account, roomID, siteID), body, nil

	case RoomCreateKind:
		// Generated room ID + name carry the prefix so they're trivially
		// identifiable in forensic Mongo audits. Users[] is empty for now;
		// real load tests can extend the args struct to include a small
		// member list when richer create payloads matter.
		newID := args.WriteIDPrefix + idgen.GenerateID()
		body, err := json.Marshal(model.CreateRoomRequest{
			Name:        newID,
			RoomID:      newID,
			RequesterID: args.User.ID,
		})
		if err != nil {
			return "", nil, fmt.Errorf("marshal CreateRoomRequest: %w", err)
		}
		return subject.RoomCreate(account, siteID), body, nil

	case MemberAddKind:
		mem := args.MemberAccount
		if mem == "" {
			mem = args.User.Account // fallback: re-add self
		}
		body, err := json.Marshal(model.AddMembersRequest{
			RoomID: roomID,
			Users:  []string{mem},
		})
		if err != nil {
			return "", nil, fmt.Errorf("marshal AddMembersRequest: %w", err)
		}
		return subject.MemberAdd(account, roomID, siteID), body, nil

	default:
		return "", nil, fmt.Errorf("%w: %d", ErrUnknownRoomKind, kind)
	}
}
