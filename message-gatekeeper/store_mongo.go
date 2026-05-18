package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

type MongoStore struct {
	subscriptions *mongo.Collection
	rooms         *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		rooms:         db.Collection("rooms"),
	}
}

func (s *MongoStore) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.account": account, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("user %s not subscribed to room %s: %w", account, roomID, errNotSubscribed)
		}
		return nil, fmt.Errorf("find subscription for user %s in room %s: %w", account, roomID, err)
	}
	return &sub, nil
}

func (s *MongoStore) GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error) {
	opts := options.FindOne().SetProjection(bson.M{
		"type":      1,
		"name":      1,
		"siteId":    1,
		"userCount": 1,
	})
	var doc struct {
		ID        string         `bson:"_id"`
		Type      model.RoomType `bson:"type"`
		Name      string         `bson:"name"`
		SiteID    string         `bson:"siteId"`
		UserCount int            `bson:"userCount"`
	}
	if err := s.rooms.FindOne(ctx, bson.M{"_id": roomID}, opts).Decode(&doc); err != nil {
		return roommetacache.Meta{}, fmt.Errorf("get room meta %q: %w", roomID, err)
	}
	return roommetacache.Meta{
		ID:        doc.ID,
		Type:      doc.Type,
		Name:      doc.Name,
		SiteID:    doc.SiteID,
		UserCount: doc.UserCount,
	}, nil
}
