package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

type MongoStore struct {
	subscriptions *mongo.Collection
	rooms         *mongo.Collection
	roomMembers   *mongo.Collection
	users         *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		rooms:         db.Collection("rooms"),
		roomMembers:   db.Collection("room_members"),
		users:         db.Collection("users"),
	}
}

func (s *MongoStore) CreateSubscription(ctx context.Context, sub *model.Subscription) error {
	_, err := s.subscriptions.InsertOne(ctx, sub)
	return err
}

func (s *MongoStore) ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error) {
	cursor, err := s.subscriptions.Find(ctx, bson.M{"roomId": roomID})
	if err != nil {
		return nil, err
	}
	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, err
	}
	return subs, nil
}

func (s *MongoStore) IncrementUserCount(ctx context.Context, roomID string, count int) error {
	_, err := s.rooms.UpdateOne(ctx, bson.M{"_id": roomID}, bson.M{"$inc": bson.M{"userCount": count}})
	return err
}

func (s *MongoStore) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	var room model.Room
	if err := s.rooms.FindOne(ctx, bson.M{"_id": roomID}).Decode(&room); err != nil {
		return nil, fmt.Errorf("room %q not found: %w", roomID, err)
	}
	return &room, nil
}

func (s *MongoStore) GetUser(ctx context.Context, account string) (*model.User, error) {
	var user model.User
	if err := s.users.FindOne(ctx, bson.M{"account": account}).Decode(&user); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("user %q not found: %w", account, err)
		}
		return nil, fmt.Errorf("get user %q: %w", account, err)
	}
	return &user, nil
}

func (s *MongoStore) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.account": account, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("%q in room %q: %w", account, roomID, model.ErrSubscriptionNotFound)
		}
		return nil, fmt.Errorf("get subscription for %q in room %q: %w", account, roomID, err)
	}
	return &sub, nil
}

func (s *MongoStore) AddRole(ctx context.Context, account, roomID string, role model.Role) error {
	filter := bson.M{"u.account": account, "roomId": roomID}
	update := bson.M{"$addToSet": bson.M{"roles": role}}
	res, err := s.subscriptions.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("add role %q for %q in room %q: %w", role, account, roomID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("subscription not found for %q in room %q", account, roomID)
	}
	return nil
}

func (s *MongoStore) RemoveRole(ctx context.Context, account, roomID string, role model.Role) error {
	filter := bson.M{"u.account": account, "roomId": roomID}
	update := bson.M{"$pull": bson.M{"roles": role}}
	res, err := s.subscriptions.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("remove role %q for %q in room %q: %w", role, account, roomID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("subscription not found for %q in room %q", account, roomID)
	}
	return nil
}

func (s *MongoStore) GetUserWithMembership(ctx context.Context, roomID, account string) (*UserWithMembership, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"account": account}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "room_members",
			"let":  bson.M{"sectId": "$sectId"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$rid", roomID}},
					bson.M{"$eq": bson.A{"$member.type", "org"}},
					bson.M{"$eq": bson.A{"$member.id", "$$sectId"}},
				}}}},
				bson.M{"$limit": 1},
			},
			"as": "orgMembership",
		}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "subscriptions",
			"let":  bson.M{"acct": "$account"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$roomId", roomID}},
					bson.M{"$eq": bson.A{"$u.account", "$$acct"}},
				}}}},
				bson.M{"$limit": 1},
				bson.M{"$project": bson.M{"roles": 1}},
			},
			"as": "targetSub",
		}}},
		{{Key: "$addFields", Value: bson.M{
			"hasOrgMembership": bson.M{"$gt": bson.A{bson.M{"$size": "$orgMembership"}, 0}},
			"roles": bson.M{"$ifNull": bson.A{
				bson.M{"$arrayElemAt": bson.A{"$targetSub.roles", 0}},
				bson.A{},
			}},
		}}},
		{{Key: "$project", Value: bson.M{"orgMembership": 0, "targetSub": 0}}},
	}
	cursor, err := s.users.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate user with membership: %w", err)
	}
	defer cursor.Close(ctx)
	var result UserWithMembership
	if !cursor.Next(ctx) {
		if err := cursor.Err(); err != nil {
			return nil, fmt.Errorf("iterate user with membership: %w", err)
		}
		return nil, fmt.Errorf("user %q not found: %w", account, mongo.ErrNoDocuments)
	}
	if err := cursor.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode user with membership: %w", err)
	}
	return &result, nil
}

func (s *MongoStore) GetOrgMembersWithIndividualStatus(ctx context.Context, roomID, orgID string) ([]OrgMemberStatus, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"sectId": orgID}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "room_members",
			"let":  bson.M{"acct": "$account"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$rid", roomID}},
					bson.M{"$eq": bson.A{"$member.type", "individual"}},
					bson.M{"$eq": bson.A{"$member.account", "$$acct"}},
				}}}},
				bson.M{"$limit": 1},
			},
			"as": "individualMembership",
		}}},
		{{Key: "$project", Value: bson.M{
			"account":                 1,
			"siteId":                  1,
			"sectName":                1,
			"hasIndividualMembership": bson.M{"$gt": bson.A{bson.M{"$size": "$individualMembership"}, 0}},
		}}},
	}
	cursor, err := s.users.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate org members: %w", err)
	}
	defer cursor.Close(ctx)
	var results []OrgMemberStatus
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode org members: %w", err)
	}
	return results, nil
}

func (s *MongoStore) DeleteSubscription(ctx context.Context, roomID, account string) (int64, error) {
	res, err := s.subscriptions.DeleteOne(ctx, bson.M{"roomId": roomID, "u.account": account})
	if err != nil {
		return 0, fmt.Errorf("delete subscription for %q in room %q: %w", account, roomID, err)
	}
	return res.DeletedCount, nil
}

func (s *MongoStore) DeleteSubscriptionsByAccounts(ctx context.Context, roomID string, accounts []string) (int64, error) {
	res, err := s.subscriptions.DeleteMany(ctx, bson.M{"roomId": roomID, "u.account": bson.M{"$in": accounts}})
	if err != nil {
		return 0, fmt.Errorf("delete subscriptions for room %q: %w", roomID, err)
	}
	return res.DeletedCount, nil
}

func (s *MongoStore) DeleteRoomMember(ctx context.Context, roomID string, memberType model.RoomMemberType, memberID string) error {
	_, err := s.roomMembers.DeleteOne(ctx, bson.M{"rid": roomID, "member.type": memberType, "member.id": memberID})
	if err != nil {
		return fmt.Errorf("delete room member: %w", err)
	}
	return nil
}

func (s *MongoStore) DecrementUserCount(ctx context.Context, roomID string, count int) error {
	_, err := s.rooms.UpdateOne(ctx, bson.M{"_id": roomID}, bson.M{"$inc": bson.M{"userCount": -count}})
	if err != nil {
		return fmt.Errorf("decrement user count for room %q: %w", roomID, err)
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
	_, err := s.subscriptions.InsertMany(ctx, docs)
	return err
}

func (s *MongoStore) CreateRoomMember(ctx context.Context, member *model.RoomMember) error {
	_, err := s.roomMembers.InsertOne(ctx, member)
	return err
}

func (s *MongoStore) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	cursor, err := s.users.Find(ctx, bson.M{"account": bson.M{"$in": accounts}})
	if err != nil {
		return nil, fmt.Errorf("find users by accounts: %w", err)
	}
	var users []model.User
	if err := cursor.All(ctx, &users); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}
	return users, nil
}

func (s *MongoStore) HasOrgRoomMembers(ctx context.Context, roomID string) (bool, error) {
	count, err := s.roomMembers.CountDocuments(ctx, bson.M{"rid": roomID})
	if err != nil {
		return false, fmt.Errorf("count room members for %q: %w", roomID, err)
	}
	return count > 0, nil
}
