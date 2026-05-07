package main

import (
	"context"
	"errors"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/model/cassandra"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store,ParentMessageFetcher

// errNotSubscribed is returned when the user is not subscribed to the room.
var errNotSubscribed = errors.New("not subscribed")

// codedError pairs a stable wire code with a user-safe message. Returned by
// validation paths that want the reply to carry a machine-readable code.
type codedError struct {
	Code    string
	Message string
}

func (e *codedError) Error() string { return e.Message }

// errLargeRoomPostRestricted is returned when a non-owner attempts to post a
// top-level message in a room whose userCount exceeds the configured
// threshold.
var errLargeRoomPostRestricted = &codedError{
	Code:    "large_room_post_restricted",
	Message: "only owners can post in this room",
}

type Store interface {
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
}

// ParentMessageFetcher resolves a quoted parent message into a snapshot
// suitable for embedding on the new message's canonical event. Implementations
// should treat any failure (not found, RPC timeout, forbidden, etc.) as a
// reason to return an error — the handler soft-fails on every error and ships
// the message without the quote.
type ParentMessageFetcher interface {
	FetchQuotedParent(ctx context.Context, account, roomID, siteID, messageID string) (*cassandra.QuotedParentMessage, error)
}
