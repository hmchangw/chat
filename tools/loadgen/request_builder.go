package main

import (
	"encoding/json"
	"fmt"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// loadHistoryRequestWire mirrors history-service's LoadHistoryRequest. The
// source struct lives under history-service/internal/models and isn't
// importable; the field tags here are the wire contract.
type loadHistoryRequestWire struct {
	Before *int64 `json:"before,omitempty"`
	Limit  int    `json:"limit"`
}

type getMessageByIDRequestWire struct {
	MessageID string `json:"messageId"`
}

type loadSurroundingRequestWire struct {
	MessageID string `json:"messageId"`
	Limit     int    `json:"limit"`
}

type getThreadMessagesRequestWire struct {
	ThreadMessageID string `json:"threadMessageId"`
	Cursor          string `json:"cursor,omitempty"`
	Limit           int    `json:"limit"`
}

// historyRequestArgs carries the per-tick inputs the history-read scenario
// uses to build a request. MessageID is consulted only by request kinds
// that need it (everything except plain LoadHistory).
type historyRequestArgs struct {
	User      model.User
	Room      model.Room
	MessageID string
	Limit     int
}

// buildHistoryRequest builds a NATS request payload + subject for the given
// history-service request kind. The returned subject targets the user's
// account scope; the body is JSON-encoded against the wire-contract structs
// above.
func buildHistoryRequest(kind historyRequestKind, args historyRequestArgs) (string, []byte, error) {
	account := args.User.Account
	roomID := args.Room.ID
	siteID := args.Room.SiteID
	limit := args.Limit
	if limit <= 0 {
		limit = 50
	}

	switch kind {
	case HistoryLoadHistory:
		body, err := json.Marshal(loadHistoryRequestWire{Limit: limit})
		if err != nil {
			return "", nil, fmt.Errorf("marshal LoadHistory: %w", err)
		}
		return subject.MsgHistory(account, roomID, siteID), body, nil
	case HistoryGetMessageByID:
		body, err := json.Marshal(getMessageByIDRequestWire{MessageID: args.MessageID})
		if err != nil {
			return "", nil, fmt.Errorf("marshal GetMessageByID: %w", err)
		}
		return subject.MsgGet(account, roomID, siteID), body, nil
	case HistoryLoadSurrounding:
		body, err := json.Marshal(loadSurroundingRequestWire{MessageID: args.MessageID, Limit: limit})
		if err != nil {
			return "", nil, fmt.Errorf("marshal LoadSurroundingMessages: %w", err)
		}
		return subject.MsgSurrounding(account, roomID, siteID), body, nil
	case HistoryGetThreadMessages:
		body, err := json.Marshal(getThreadMessagesRequestWire{ThreadMessageID: args.MessageID, Limit: limit})
		if err != nil {
			return "", nil, fmt.Errorf("marshal GetThreadMessages: %w", err)
		}
		return subject.MsgThread(account, roomID, siteID), body, nil
	default:
		return "", nil, fmt.Errorf("unknown historyRequestKind: %d", kind)
	}
}

// searchRequestArgs carries the per-tick inputs the search-read scenario
// uses to build a request. Scope is consulted only by SearchRoomsKind.
type searchRequestArgs struct {
	User  model.User
	Query string
	Scope string
	Size  int
}

// buildSearchRequest builds a NATS request payload + subject for the given
// search-service request kind. Reuses the public model.SearchMessagesRequest
// and model.SearchRoomsRequest shapes.
func buildSearchRequest(kind searchRequestKind, args searchRequestArgs) (string, []byte, error) {
	switch kind {
	case SearchMessagesKind:
		body, err := json.Marshal(model.SearchMessagesRequest{
			SearchText: args.Query,
			Size:       args.Size,
		})
		if err != nil {
			return "", nil, fmt.Errorf("marshal SearchMessages: %w", err)
		}
		return subject.SearchMessages(args.User.Account), body, nil
	case SearchRoomsKind:
		body, err := json.Marshal(model.SearchRoomsRequest{
			SearchText: args.Query,
			Scope:      args.Scope,
			Size:       args.Size,
		})
		if err != nil {
			return "", nil, fmt.Errorf("marshal SearchRooms: %w", err)
		}
		return subject.SearchRooms(args.User.Account), body, nil
	default:
		return "", nil, fmt.Errorf("unknown searchRequestKind: %d", kind)
	}
}
