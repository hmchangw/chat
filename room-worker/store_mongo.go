package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

type MongoStore struct {
	subscriptions *mongo.Collection
	rooms         *mongo.Collection
	roomMembers   *mongo.Collection
	users         *mongo.Collection
	hrData        *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		rooms:         db.Collection("rooms"),
		roomMembers:   db.Collection("room_members"),
		users:         db.Collection("users"),
		hrData:        db.Collection("hr_data"),
	}
}

func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	_, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "roomId", Value: 1}, {Key: "u.username", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("create subscriptions username index: %w", err)
	}
	_, err = s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "roomId", Value: 1}, {Key: "u.account", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("create subscriptions account index: %w", err)
	}

	_, err = s.roomMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "rid", Value: 1}, {Key: "member.type", Value: 1}, {Key: "member.id", Value: 1}, {Key: "member.username", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("create room_members index: %w", err)
	}

	return nil
}

func (s *MongoStore) CreateSubscription(ctx context.Context, sub *model.Subscription) error {
	_, err := s.subscriptions.InsertOne(ctx, sub)
	if err != nil {
		return fmt.Errorf("insert subscription: %w", err)
	}
	return nil
}

func (s *MongoStore) BulkCreateSubscriptions(ctx context.Context, subs []*model.Subscription) error {
	if len(subs) == 0 {
		return nil
	}
	docs := make([]interface{}, len(subs))
	for i, sub := range subs {
		docs[i] = sub
	}
	_, err := s.subscriptions.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("bulk insert subscriptions: %w", err)
	}
	return nil
}

func (s *MongoStore) DeleteSubscription(ctx context.Context, account, roomID string) error {
	_, err := s.subscriptions.DeleteOne(ctx, bson.M{"u.account": account, "roomId": roomID})
	if err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	return nil
}

func (s *MongoStore) UpdateSubscriptionRole(ctx context.Context, account, roomID string, role model.Role) error {
	_, err := s.subscriptions.UpdateOne(ctx,
		bson.M{"u.account": account, "roomId": roomID},
		bson.M{"$set": bson.M{"role": role}},
	)
	if err != nil {
		return fmt.Errorf("update subscription role: %w", err)
	}
	return nil
}

func (s *MongoStore) ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error) {
	cursor, err := s.subscriptions.Find(ctx, bson.M{"roomId": roomID})
	if err != nil {
		return nil, fmt.Errorf("find subscriptions by room: %w", err)
	}
	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, fmt.Errorf("decode subscriptions: %w", err)
	}
	return subs, nil
}

func (s *MongoStore) IncrementUserCount(ctx context.Context, roomID string) error {
	_, err := s.rooms.UpdateOne(ctx, bson.M{"_id": roomID}, bson.M{"$inc": bson.M{"userCount": 1}})
	if err != nil {
		return fmt.Errorf("increment user count: %w", err)
	}
	return nil
}

func (s *MongoStore) DecrementUserCount(ctx context.Context, roomID string) error {
	_, err := s.rooms.UpdateOne(ctx, bson.M{"_id": roomID}, bson.M{"$inc": bson.M{"userCount": -1}})
	if err != nil {
		return fmt.Errorf("decrement user count: %w", err)
	}
	return nil
}

func (s *MongoStore) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	var room model.Room
	if err := s.rooms.FindOne(ctx, bson.M{"_id": roomID}).Decode(&room); err != nil {
		return nil, fmt.Errorf("find room %q: %w", roomID, err)
	}
	return &room, nil
}

func (s *MongoStore) CreateRoomMember(ctx context.Context, member *model.RoomMember) error {
	_, err := s.roomMembers.InsertOne(ctx, member)
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("insert room member: %w", err)
	}
	return nil
}

func (s *MongoStore) DeleteRoomMember(ctx context.Context, account, roomID string) error {
	_, err := s.roomMembers.DeleteOne(ctx, bson.M{"member.username": account, "rid": roomID})
	if err != nil {
		return fmt.Errorf("delete room member: %w", err)
	}
	return nil
}

func (s *MongoStore) DeleteOrgRoomMember(ctx context.Context, orgID, roomID string) error {
	_, err := s.roomMembers.DeleteOne(ctx, bson.M{"member.id": orgID, "rid": roomID})
	if err != nil {
		return fmt.Errorf("delete org room member: %w", err)
	}
	return nil
}

func (s *MongoStore) GetUser(ctx context.Context, account string) (*model.User, error) {
	var user model.User
	if err := s.users.FindOne(ctx, bson.M{"account": account}).Decode(&user); err != nil {
		return nil, fmt.Errorf("find user %q: %w", account, err)
	}
	return &user, nil
}

func (s *MongoStore) GetOrgAccounts(ctx context.Context, orgID string) ([]string, error) {
	cursor, err := s.hrData.Find(ctx, bson.M{"sectId": orgID})
	if err != nil {
		return nil, fmt.Errorf("find org accounts: %w", err)
	}
	var results []struct {
		AccountName string `bson:"accountName"`
	}
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode org accounts: %w", err)
	}
	accounts := make([]string, len(results))
	for i, r := range results {
		accounts[i] = r.AccountName
	}
	return accounts, nil
}

func (s *MongoStore) GetRoomMembers(ctx context.Context, roomID string) ([]model.RoomMember, error) {
	cursor, err := s.roomMembers.Find(ctx, bson.M{"rid": roomID})
	if err != nil {
		return nil, fmt.Errorf("find room members: %w", err)
	}
	var members []model.RoomMember
	if err := cursor.All(ctx, &members); err != nil {
		return nil, fmt.Errorf("decode room members: %w", err)
	}
	return members, nil
}
