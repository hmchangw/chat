package main

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

type mongoStore struct {
	roomCol *mongo.Collection
	subCol  *mongo.Collection
	empCol  *mongo.Collection
}

func NewMongoStore(roomCol, subCol, empCol *mongo.Collection) *mongoStore {
	return &mongoStore{roomCol: roomCol, subCol: subCol, empCol: empCol}
}

func (m *mongoStore) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	filter := bson.M{"_id": roomID}
	var room model.Room
	if err := m.roomCol.FindOne(ctx, filter).Decode(&room); err != nil {
		return nil, fmt.Errorf("find room %s: %w", roomID, err)
	}
	return &room, nil
}

func (m *mongoStore) ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error) {
	filter := bson.M{"roomId": roomID}
	cursor, err := m.subCol.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("query subscriptions for room %s: %w", roomID, err)
	}
	defer cursor.Close(ctx)
	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, fmt.Errorf("decode subscriptions: %w", err)
	}
	return subs, nil
}

func (m *mongoStore) UpdateRoomOnNewMessage(ctx context.Context, roomID string, msgID string, msgAt time.Time, mentionAll bool) error {
	fields := bson.M{
		"lastMsgAt": msgAt,
		"lastMsgId": msgID,
		"updatedAt": msgAt,
	}
	if mentionAll {
		fields["lastMentionAllAt"] = msgAt
	}
	filter := bson.M{"_id": roomID}
	update := bson.M{"$set": fields}
	_, err := m.roomCol.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update room %s on new message: %w", roomID, err)
	}
	return nil
}

func (m *mongoStore) SetSubscriptionMentions(ctx context.Context, roomID string, accounts []string) error {
	filter := bson.M{
		"roomId":    roomID,
		"u.account": bson.M{"$in": accounts},
	}
	update := bson.M{"$set": bson.M{"hasMention": true}}
	_, err := m.subCol.UpdateMany(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("set subscription mentions for room %s: %w", roomID, err)
	}
	return nil
}

func (m *mongoStore) FindEmployeesByAccountNames(ctx context.Context, accountNames []string) ([]model.Employee, error) {
	filter := bson.M{"accountName": bson.M{"$in": accountNames}}
	projection := bson.M{"accountName": 1, "name": 1, "engName": 1, "_id": 0}
	opts := options.Find().SetProjection(projection)
	cursor, err := m.empCol.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("query employees by account names: %w", err)
	}
	defer cursor.Close(ctx)
	var employees []model.Employee
	if err := cursor.All(ctx, &employees); err != nil {
		return nil, fmt.Errorf("decode employees: %w", err)
	}
	return employees, nil
}
