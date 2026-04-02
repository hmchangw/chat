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
	orgs          *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		rooms:         db.Collection("rooms"),
		subscriptions: db.Collection("subscriptions"),
		roomMembers:   db.Collection("room_members"),
		hrData:        db.Collection("hr_data"),
		users:         db.Collection("users"),
		orgs:          db.Collection("orgs"),
	}
}

// EnsureIndexes creates unique indexes on subscriptions and room_members.
func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	_, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "u.username", Value: 1}, {Key: "roomId", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("create subscriptions index: %w", err)
	}
	_, err = s.roomMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "member.username", Value: 1}, {Key: "rid", Value: 1}},
		Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.M{"member.username": bson.M{"$exists": true}}),
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

func (s *MongoStore) CreateRoomMember(ctx context.Context, member *model.RoomMember) error {
	_, err := s.roomMembers.InsertOne(ctx, member)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil // idempotent: already exists
		}
		return fmt.Errorf("insert room member: %w", err)
	}
	return nil
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
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil // idempotent: duplicates ignored
		}
		return fmt.Errorf("bulk insert subscriptions: %w", err)
	}
	return nil
}

func (s *MongoStore) GetOrgData(ctx context.Context, orgID string) (name, locationURL string, err error) {
	var doc struct {
		Name        string `bson:"name"`
		LocationURL string `bson:"locationUrl"`
	}
	if err := s.orgs.FindOne(ctx, bson.M{"_id": orgID}).Decode(&doc); err != nil {
		return "", "", fmt.Errorf("get org data for %q: %w", orgID, err)
	}
	return doc.Name, doc.LocationURL, nil
}

func (s *MongoStore) GetUserID(ctx context.Context, username string) (string, error) {
	var doc struct {
		ID string `bson:"_id"`
	}
	if err := s.users.FindOne(ctx, bson.M{"username": username}).Decode(&doc); err != nil {
		return "", fmt.Errorf("get user id for %q: %w", username, err)
	}
	return doc.ID, nil
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

func (s *MongoStore) DeleteSubscription(ctx context.Context, username, roomID string) error {
	_, err := s.subscriptions.DeleteOne(ctx, bson.M{"u.username": username, "roomId": roomID})
	if err != nil {
		return fmt.Errorf("delete subscription for %q in room %q: %w", username, roomID, err)
	}
	return nil
}

func (s *MongoStore) DeleteRoomMember(ctx context.Context, username, roomID string) error {
	_, err := s.roomMembers.DeleteOne(ctx, bson.M{"member.username": username, "rid": roomID})
	if err != nil {
		return fmt.Errorf("delete room member %q in room %q: %w", username, roomID, err)
	}
	return nil
}

func (s *MongoStore) DeleteOrgRoomMember(ctx context.Context, orgID, roomID string) error {
	_, err := s.roomMembers.DeleteOne(ctx, bson.M{"member.id": orgID, "rid": roomID})
	if err != nil {
		return fmt.Errorf("delete org room member %q in room %q: %w", orgID, roomID, err)
	}
	return nil
}

func (s *MongoStore) UpdateSubscriptionRole(ctx context.Context, username, roomID string, role model.Role) error {
	filter := bson.M{"u.username": username, "roomId": roomID}
	update := bson.M{"$set": bson.M{"roles": []model.Role{role}}}
	result, err := s.subscriptions.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update role for %q in room %q: %w", username, roomID, err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("subscription not found for %q in room %q", username, roomID)
	}
	return nil
}

func (s *MongoStore) CountOwners(ctx context.Context, roomID string) (int, error) {
	filter := bson.M{"roomId": roomID, "roles": model.RoleOwner}
	count, err := s.subscriptions.CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("count owners for room %q: %w", roomID, err)
	}
	return int(count), nil
}

func (s *MongoStore) BulkDeleteSubscriptions(ctx context.Context, subs []*model.Subscription) error {
	if len(subs) == 0 {
		return nil
	}
	ids := make([]string, len(subs))
	for i, sub := range subs {
		ids[i] = sub.ID
	}
	_, err := s.subscriptions.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return fmt.Errorf("bulk delete subscriptions: %w", err)
	}
	return nil
}

func (s *MongoStore) DecrementUserCount(ctx context.Context, roomID string) error {
	filter := bson.M{"_id": roomID}
	update := bson.M{"$inc": bson.M{"userCount": -1}}
	_, err := s.rooms.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("decrement user count for room %q: %w", roomID, err)
	}
	return nil
}
