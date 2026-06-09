package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store,ThreadStore
//go:generate mockgen -destination=mock_userstore_test.go -package=main github.com/hmchangw/chat/pkg/userstore UserStore

// Store defines Cassandra persistence operations for the message worker.
type Store interface {
	SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error
	SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string, threadRoomID string) error
	GetMessageSender(ctx context.Context, messageID string) (*cassParticipant, error)
	UpdateParentMessageThreadRoomID(ctx context.Context, parentMessageID, roomID string, parentCreatedAt time.Time, threadRoomID string) error
	// UpdateParentTcount mirrors the authoritative reply count (sourced from MongoDB) onto the parent message row in both Cassandra tables with plain (non-LWT) UPDATEs.
	UpdateParentTcount(ctx context.Context, roomID, parentID string, parentCreatedAt time.Time, count int) error
}

// ThreadStore defines MongoDB operations for thread room and subscription management.
type ThreadStore interface {
	CreateThreadRoom(ctx context.Context, room *model.ThreadRoom) error
	GetThreadRoomByParentMessageID(ctx context.Context, parentMessageID string) (*model.ThreadRoom, error)
	InsertThreadSubscription(ctx context.Context, sub *model.ThreadSubscription) error
	UpsertThreadSubscription(ctx context.Context, sub *model.ThreadSubscription) error
	MarkThreadSubscriptionMention(ctx context.Context, sub *model.ThreadSubscription) error
	// UpdateThreadRoomLastMessage atomically increments replyCount only if replyID has not
	// already been counted, records replyID in the bounded countedReplies guard, bumps the
	// last-message pointer, and merges replyAccounts. Returns (newCount, applied=true, nil)
	// on first delivery; on redelivery (replyID already in countedReplies) returns (0, false, nil).
	UpdateThreadRoomLastMessage(ctx context.Context, threadRoomID, lastMsgID string, replyAccounts []string, lastMsgAt time.Time, replyID string) (int, bool, error)
	// AddReplyAccounts $addToSet-merges accounts into thread_rooms.replyAccounts.
	// Used by paths that don't already update lastMsg (first-reply parent author,
	// mention-only subscribers) so the field mirrors thread_subscriptions membership.
	AddReplyAccounts(ctx context.Context, threadRoomID string, accounts []string) error
}
