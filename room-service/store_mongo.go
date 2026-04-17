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
	rooms         *mongo.Collection
	subscriptions *mongo.Collection
	roomMembers   *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		rooms:         db.Collection("rooms"),
		subscriptions: db.Collection("subscriptions"),
		roomMembers:   db.Collection("room_members"),
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

func (s *MongoStore) CreateSubscription(ctx context.Context, sub *model.Subscription) error {
	_, err := s.subscriptions.InsertOne(ctx, sub)
	return err
}

// GetSubscriptionWithMembership loads the target subscription joined with their
// individual and org membership sources. Used by the remove-member validation
// flow to decide whether a user can leave or be removed individually.
func (s *MongoStore) GetSubscriptionWithMembership(ctx context.Context, roomID, account string) (*SubscriptionWithMembership, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"roomId": roomID, "u.account": account}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "room_members",
			"let":  bson.M{"acct": "$u.account"},
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
		{{Key: "$lookup", Value: bson.M{
			"from": "users",
			"let":  bson.M{"acct": "$u.account"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$eq": bson.A{"$account", "$$acct"}}}},
				bson.M{"$limit": 1},
				bson.M{"$project": bson.M{"sectId": 1}},
			},
			"as": "userDoc",
		}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "room_members",
			"let":  bson.M{"sectId": bson.M{"$arrayElemAt": bson.A{"$userDoc.sectId", 0}}},
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
		{{Key: "$addFields", Value: bson.M{
			"hasIndividualMembership": bson.M{"$gt": bson.A{bson.M{"$size": "$individualMembership"}, 0}},
			"hasOrgMembership":        bson.M{"$gt": bson.A{bson.M{"$size": "$orgMembership"}, 0}},
		}}},
		{{Key: "$project", Value: bson.M{"individualMembership": 0, "orgMembership": 0, "userDoc": 0}}},
	}

	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate subscription with membership: %w", err)
	}
	defer cursor.Close(ctx)

	var result struct {
		model.Subscription      `bson:",inline"`
		HasIndividualMembership bool `bson:"hasIndividualMembership"`
		HasOrgMembership        bool `bson:"hasOrgMembership"`
	}
	if !cursor.Next(ctx) {
		if err := cursor.Err(); err != nil {
			return nil, fmt.Errorf("iterate subscription with membership: %w", err)
		}
		return nil, fmt.Errorf("subscription not found for account %q in room %q: %w", account, roomID, mongo.ErrNoDocuments)
	}
	if err := cursor.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode subscription with membership: %w", err)
	}
	sub := result.Subscription
	return &SubscriptionWithMembership{
		Subscription:            &sub,
		HasIndividualMembership: result.HasIndividualMembership,
		HasOrgMembership:        result.HasOrgMembership,
	}, nil
}

// CountMembersAndOwners returns the total and owner-role subscription counts
// for a room in a single aggregation, driving the last-owner and last-member
// guards in remove-member validation.
func (s *MongoStore) CountMembersAndOwners(ctx context.Context, roomID string) (*RoomCounts, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"roomId": roomID}}},
		{{Key: "$facet", Value: bson.M{
			"members": bson.A{bson.M{"$count": "count"}},
			"owners": bson.A{
				bson.M{"$match": bson.M{"roles": model.RoleOwner}},
				bson.M{"$count": "count"},
			},
		}}},
	}
	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate room counts: %w", err)
	}
	defer cursor.Close(ctx)

	var result struct {
		Members []struct {
			Count int `bson:"count"`
		} `bson:"members"`
		Owners []struct {
			Count int `bson:"count"`
		} `bson:"owners"`
	}
	if !cursor.Next(ctx) {
		if err := cursor.Err(); err != nil {
			return nil, fmt.Errorf("iterate room counts: %w", err)
		}
		return &RoomCounts{}, nil
	}
	if err := cursor.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode room counts: %w", err)
	}
	counts := &RoomCounts{}
	if len(result.Members) > 0 {
		counts.MemberCount = result.Members[0].Count
	}
	if len(result.Owners) > 0 {
		counts.OwnerCount = result.Owners[0].Count
	}
	return counts, nil
}

func (s *MongoStore) CountOwners(ctx context.Context, roomID string) (int, error) {
	count, err := s.subscriptions.CountDocuments(ctx, bson.M{"roomId": roomID, "roles": model.RoleOwner})
	if err != nil {
		return 0, fmt.Errorf("count owners for room %q: %w", roomID, err)
	}
	return int(count), nil
}
