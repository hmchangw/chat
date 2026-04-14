package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

var errThreadRoomExists = errors.New("thread room already exists")

type threadStoreMongo struct {
	threadRooms         *mongo.Collection
	threadSubscriptions *mongo.Collection
}

// Compile-time assertion that *threadStoreMongo satisfies ThreadStore.
var _ ThreadStore = (*threadStoreMongo)(nil)

//nolint:unused // wired up by main.go in a subsequent change; integration tests exercise it under the `integration` build tag.
func newThreadStoreMongo(ctx context.Context, db *mongo.Database) (*threadStoreMongo, error) {
	threadRooms := db.Collection("threadRooms")
	if _, err := threadRooms.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "parentMessageId", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return nil, fmt.Errorf("create threadRooms index: %w", err)
	}

	threadSubs := db.Collection("threadSubscriptions")
	if _, err := threadSubs.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "threadRoomId", Value: 1}, {Key: "userId", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return nil, fmt.Errorf("create threadSubscriptions index: %w", err)
	}

	return &threadStoreMongo{
		threadRooms:         threadRooms,
		threadSubscriptions: threadSubs,
	}, nil
}

func (s *threadStoreMongo) CreateThreadRoom(ctx context.Context, room *model.ThreadRoom) error {
	_, err := s.threadRooms.InsertOne(ctx, room)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return errThreadRoomExists
		}
		return fmt.Errorf("insert thread room: %w", err)
	}
	return nil
}

func (s *threadStoreMongo) GetThreadRoomByParentMessageID(ctx context.Context, parentMessageID string) (*model.ThreadRoom, error) {
	var room model.ThreadRoom
	if err := s.threadRooms.FindOne(ctx, bson.M{"parentMessageId": parentMessageID}).Decode(&room); err != nil {
		return nil, fmt.Errorf("find thread room by parent %s: %w", parentMessageID, err)
	}
	return &room, nil
}

func (s *threadStoreMongo) UpsertThreadSubscription(ctx context.Context, sub *model.ThreadSubscription) error {
	filter := bson.M{"threadRoomId": sub.ThreadRoomID, "userId": sub.UserID}
	update := bson.M{
		"$set": bson.M{
			"parentMessageId": sub.ParentMessageID,
			"roomId":          sub.RoomID,
			"userAccount":     sub.UserAccount,
			"siteId":          sub.SiteID,
			"lastSeenAt":      sub.LastSeenAt,
			"updatedAt":       sub.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"_id":       sub.ID,
			"createdAt": sub.CreatedAt,
		},
	}
	_, err := s.threadSubscriptions.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert thread subscription: %w", err)
	}
	return nil
}

func (s *threadStoreMongo) UpdateThreadRoomLastMessage(ctx context.Context, threadRoomID string, lastMsgID string, lastMsgAt time.Time) error {
	_, err := s.threadRooms.UpdateOne(ctx, bson.M{"_id": threadRoomID}, bson.M{
		"$set": bson.M{
			"lastMsgAt": lastMsgAt,
			"lastMsgId": lastMsgID,
			"updatedAt": lastMsgAt,
		},
	})
	if err != nil {
		return fmt.Errorf("update thread room last message: %w", err)
	}
	return nil
}
