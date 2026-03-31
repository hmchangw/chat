package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

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

func (s *MongoStore) GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.username": username, "roomId": roomID}
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
	cursor, err := s.roomMembers.Find(ctx, bson.M{"roomId": roomID})
	if err != nil {
		return nil, fmt.Errorf("find room members: %w", err)
	}
	var members []model.RoomMember
	if err := cursor.All(ctx, &members); err != nil {
		return nil, fmt.Errorf("decode room members: %w", err)
	}
	return members, nil
}

func (s *MongoStore) CreateRoomMember(ctx context.Context, member *model.RoomMember) error {
	_, err := s.roomMembers.InsertOne(ctx, member)
	return err
}

func (s *MongoStore) BulkCreateSubscriptions(ctx context.Context, subs []*model.Subscription) error {
	if len(subs) == 0 {
		return nil
	}
	docs := make([]any, len(subs))
	for i, sub := range subs {
		docs[i] = sub
	}
	_, err := s.subscriptions.InsertMany(ctx, docs)
	return err
}

func (s *MongoStore) GetOrgUsers(ctx context.Context, orgID string) ([]string, error) {
	cursor, err := s.hrData.Find(ctx, bson.M{"sectId": orgID})
	if err != nil {
		return nil, fmt.Errorf("find org users: %w", err)
	}
	var docs []struct {
		Username string `bson:"username"`
	}
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode org users: %w", err)
	}
	usernames := make([]string, len(docs))
	for i, doc := range docs {
		usernames[i] = doc.Username
	}
	return usernames, nil
}

func (s *MongoStore) GetUserSite(ctx context.Context, username string) (string, error) {
	var doc struct {
		Federation struct {
			Origin string `bson:"origin"`
		} `bson:"federation"`
	}
	if err := s.users.FindOne(ctx, bson.M{"username": username}).Decode(&doc); err != nil {
		return "", fmt.Errorf("get user site for %q: %w", username, err)
	}
	return doc.Federation.Origin, nil
}
