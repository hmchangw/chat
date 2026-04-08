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
	rooms         *mongo.Collection
	subscriptions *mongo.Collection
	roomMembers   *mongo.Collection
	hrData        *mongo.Collection
	users         *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		rooms:         db.Collection("rooms"),
		subscriptions: db.Collection("subscriptions"),
		roomMembers:   db.Collection("room_members"),
		hrData:        db.Collection("hr_data"),
		users:         db.Collection("users"),
	}
}

// EnsureIndexes creates unique indexes on subscriptions and room_members.
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

func (s *MongoStore) CreateRoom(ctx context.Context, room *model.Room) error {
	_, err := s.rooms.InsertOne(ctx, room)
	return err
}

func (s *MongoStore) GetRoom(ctx context.Context, id string) (*model.Room, error) {
	var room model.Room
	if err := s.rooms.FindOne(ctx, bson.M{"_id": id}).Decode(&room); err != nil {
		return nil, fmt.Errorf("room %q not found: %w", id, err)
	}
	return &room, nil
}

func (s *MongoStore) ListRooms(ctx context.Context) ([]model.Room, error) {
	cursor, err := s.rooms.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	var rooms []model.Room
	if err := cursor.All(ctx, &rooms); err != nil {
		return nil, err
	}
	return rooms, nil
}

func (s *MongoStore) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.account": account, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("subscription not found: %w", err)
	}
	return &sub, nil
}

func (s *MongoStore) CreateSubscription(ctx context.Context, sub *model.Subscription) error {
	_, err := s.subscriptions.InsertOne(ctx, sub)
	return err
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

func (s *MongoStore) GetOrgAccounts(ctx context.Context, orgID string) ([]string, error) {
	cursor, err := s.hrData.Find(ctx, bson.M{"sectId": orgID})
	if err != nil {
		return nil, fmt.Errorf("get org accounts for %q: %w", orgID, err)
	}
	var docs []struct {
		AccountName string `bson:"accountName"`
	}
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode org accounts for %q: %w", orgID, err)
	}
	accounts := make([]string, len(docs))
	for i, d := range docs {
		accounts[i] = d.AccountName
	}
	return accounts, nil
}

func (s *MongoStore) CountSubscriptions(ctx context.Context, roomID string) (int, error) {
	filter := bson.M{
		"roomId": roomID,
		"u.username": bson.M{
			"$not": bson.Regex{Pattern: `(\.bot$|^p_)`, Options: ""},
		},
	}
	count, err := s.subscriptions.CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("count subscriptions for room %q: %w", roomID, err)
	}
	return int(count), nil
}

func (s *MongoStore) ListSubscriptionsByRoom(ctx context.Context, roomID string) ([]model.Subscription, error) {
	cursor, err := s.subscriptions.Find(ctx, bson.M{"roomId": roomID})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions for room %q: %w", roomID, err)
	}
	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, fmt.Errorf("decode subscriptions for room %q: %w", roomID, err)
	}
	return subs, nil
}

func (s *MongoStore) CountOwners(ctx context.Context, roomID string) (int, error) {
	filter := bson.M{"roomId": roomID, "role": model.RoleOwner}
	count, err := s.subscriptions.CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("count owners for room %q: %w", roomID, err)
	}
	return int(count), nil
}
