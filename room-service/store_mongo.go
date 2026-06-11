package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/displayfmt"
	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/pipelines"
)

// botAccountRegex matches bot/app accounts by the ".bot" suffix.
// helper.go::botPattern also matches "^p_" but p_ accounts are platform admins, not bots.
const botAccountRegex = `\.bot$`

var botAccountPattern = regexp.MustCompile(botAccountRegex)

type MongoStore struct {
	rooms               *mongo.Collection
	subscriptions       *mongo.Collection
	threadSubscriptions *mongo.Collection
	roomMembers         *mongo.Collection
	users               *mongo.Collection
	apps                *mongo.Collection
	botCmdMenus         *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{
		rooms:               db.Collection("rooms"),
		subscriptions:       db.Collection("subscriptions"),
		threadSubscriptions: db.Collection("thread_subscriptions"),
		roomMembers:         db.Collection("room_members"),
		users:               db.Collection("users"),
		apps:                db.Collection("apps"),
		botCmdMenus:         db.Collection("bot_cmd_menu"),
	}
}

// EnsureIndexes creates indexes backing read paths and unique constraints for retry-safe writes.
// Idempotent when key spec and options match; call once at startup.
func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	if _, err := s.roomMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "rid", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure room_members (rid) index: %w", err)
	}
	// Unique logical key: without this a redelivered member.add silently inserts duplicates.
	// room-worker's bulk-insert ignores IsDuplicateKeyError, so this makes redelivery idempotent.
	if _, err := s.roomMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "rid", Value: 1}, {Key: "member.type", Value: 1}, {Key: "member.id", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("ensure room_members (rid,member.type,member.id) unique index: %w", err)
	}
	// Unique logical key for subscriptions. Same retry-idempotency rationale as room_members above.
	if _, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "roomId", Value: 1}, {Key: "u.account", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("ensure subscriptions (roomId,u.account) unique index: %w", err)
	}
	// Unique: account is a user's identity; user-service also declares this unique on the shared
	// collection — both must agree or the second CreateOne hits IndexOptionsConflict.
	if _, err := s.users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "account", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		// E11000 here means pre-existing duplicate account values (populated env
		// pre-rollout) — point operators at the one-time dedupe preflight.
		if mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("ensure users (account) unique index: duplicate account values exist in the users "+
				"collection — run the one-time dedupe preflight (group users by account, resolve n>1) before "+
				"starting this service: %w", err)
		}
		// A pre-existing non-unique account_1 conflicts (85 IndexOptionsConflict /
		// 86 IndexKeySpecsConflict); Mongo won't upgrade it — the operator must drop it.
		if se := mongo.ServerError(nil); errors.As(err, &se) && (se.HasErrorCode(85) || se.HasErrorCode(86)) {
			return fmt.Errorf("ensure users (account) unique index: a non-unique account_1 index already exists on "+
				"the users collection — drop the old non-unique account_1 index (db.users.dropIndex(\"account_1\")) "+
				"before starting this service so it can be recreated as unique: %w", err)
		}
		return fmt.Errorf("ensure users (account) unique index: %w", err)
	}
	if _, err := s.users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "sectId", Value: 1}, {Key: "account", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure users (sectId,account) index: %w", err)
	}
	if _, err := s.users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "deptId", Value: 1}, {Key: "account", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure users (deptId,account) index: %w", err)
	}
	// Lookup index for botDM creation: GetApp filters by assistant.name.
	appsIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "assistant.name", Value: 1}},
		Options: options.Index().SetName("assistant_name_idx"),
	}
	if _, err := s.apps.Indexes().CreateOne(ctx, appsIndex); err != nil {
		return fmt.Errorf("ensure apps index: %w", err)
	}
	if _, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "roomId", Value: 1}, {Key: "lastSeenAt", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure subscriptions (roomId,lastSeenAt) index: %w", err)
	}
	// Backs ReconcileMemberCounts (bot vs non-bot counts per room) index-only.
	if _, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "roomId", Value: 1}, {Key: "u.isBot", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure subscriptions (roomId,u.isBot) index: %w", err)
	}
	// Lookup index for FindDMSubscription (filters on u.account+name).
	// Without this index, FindDMSubscription falls back to a collection scan.
	if _, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "u.account", Value: 1}, {Key: "name", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure subscriptions (u.account,name) index: %w", err)
	}
	// Backs getRoomSubscriptions: filter roomId, sort {joinedAt, _id} with
	// skip/limit pagination. Including the sort keys lets Mongo return ordered
	// pages from the index instead of an in-memory sort that risks the 32MB
	// sort limit on large rooms.
	if _, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "roomId", Value: 1}, {Key: "joinedAt", Value: 1}, {Key: "_id", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure subscriptions (roomId,joinedAt,_id) index: %w", err)
	}
	// Backs CountOwners (filters on roomId+roles) so owner counts stay
	// index-only instead of scanning every subscription in the room.
	if _, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "roomId", Value: 1}, {Key: "roles", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure subscriptions (roomId,roles) index: %w", err)
	}
	// Mirrors the unique index created by message-worker / history-service so per-service test DBs also enforce it.
	if _, err := s.threadSubscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "threadRoomId", Value: 1}, {Key: "userAccount", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("ensure thread_subscriptions (threadRoomId,userAccount) unique index: %w", err)
	}
	if _, err := s.threadSubscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "parentMessageId", Value: 1}, {Key: "userAccount", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure thread_subscriptions (parentMessageId,userAccount) index: %w", err)
	}
	if _, err := s.apps.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "channelTab.default", Value: 1},
			{Key: "channelTab.enabled", Value: 1},
			{Key: "channelTab.name", Value: 1},
		},
	}); err != nil {
		return fmt.Errorf("ensure apps (channelTab.default,enabled,name) index: %w", err)
	}
	if _, err := s.botCmdMenus.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "activeStatus", Value: 1}, {Key: "name", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure bot_cmd_menu (activeStatus,name) index: %w", err)
	}
	return nil
}

func (s *MongoStore) GetRoom(ctx context.Context, id string) (*model.Room, error) {
	var room model.Room
	if err := s.rooms.FindOne(ctx, bson.M{"_id": id}).Decode(&room); err != nil {
		return nil, fmt.Errorf("room %q not found: %w", id, err)
	}
	return &room, nil
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

// GetSubscriptionWithMembership loads the subscription joined with its individual and org membership
// sources, used by the remove-member validation flow.
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
				bson.M{"$project": bson.M{"sectId": 1, "deptId": 1}},
			},
			"as": "userDoc",
		}}},
		// Dept-aware lookup: a user added via deptId only has no sectId match, so checking
		// only sectId would falsely report HasOrgMembership=false and drop the subscription.
		{{Key: "$lookup", Value: bson.M{
			"from": "room_members",
			"let": bson.M{
				"sectId": bson.M{"$arrayElemAt": bson.A{"$userDoc.sectId", 0}},
				"deptId": bson.M{"$arrayElemAt": bson.A{"$userDoc.deptId", 0}},
			},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$rid", roomID}},
					bson.M{"$eq": bson.A{"$member.type", "org"}},
					bson.M{"$or": bson.A{
						bson.M{"$eq": bson.A{"$member.id", "$$sectId"}},
						bson.M{"$eq": bson.A{"$member.id", "$$deptId"}},
					}},
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

// CountMembersAndOwners returns total and owner-role counts for a room in one aggregation,
// driving last-owner and last-member guards.
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

func (s *MongoStore) ListRoomsByIDs(ctx context.Context, ids []string) ([]model.Room, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cursor, err := s.rooms.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, fmt.Errorf("list rooms by ids: %w", err)
	}
	var rooms []model.Room
	if err := cursor.All(ctx, &rooms); err != nil {
		return nil, fmt.Errorf("list rooms by ids: decode: %w", err)
	}
	return rooms, nil
}

func (s *MongoStore) CountOwners(ctx context.Context, roomID string) (int, error) {
	count, err := s.subscriptions.CountDocuments(ctx, bson.M{"roomId": roomID, "roles": model.RoleOwner})
	if err != nil {
		return 0, fmt.Errorf("count owners for room %q: %w", roomID, err)
	}
	return int(count), nil
}

func (s *MongoStore) CountNewMembers(ctx context.Context, orgIDs, directAccounts []string, roomID, excludeAccount string) (int, error) {
	if len(orgIDs) == 0 && len(directAccounts) == 0 {
		return 0, nil
	}
	pipeline := pipelines.GetNewMembersPipeline(orgIDs, directAccounts, roomID, excludeAccount)
	pipeline = append(pipeline, bson.M{"$count": "n"})

	cursor, err := s.users.Aggregate(ctx, pipeline)
	if err != nil {
		return 0, fmt.Errorf("count new members: %w", err)
	}
	var results []struct {
		Count int `bson:"n"`
	}
	if err := cursor.All(ctx, &results); err != nil {
		return 0, fmt.Errorf("decode count new members: %w", err)
	}
	if len(results) == 0 {
		return 0, nil
	}
	return results[0].Count, nil
}

// ListRoomMembers returns members from room_members, falling back to subscriptions when no
// room_members doc exists. Sort: orgs first, then individuals by ts asc with _id tiebreaker.
func (s *MongoStore) ListRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
	// Lightweight existence probe — project only _id to minimize payload.
	err := s.roomMembers.FindOne(ctx, bson.M{"rid": roomID},
		options.FindOne().SetProjection(bson.M{"_id": 1})).Err()
	switch {
	case err == nil:
		return s.getRoomMembers(ctx, roomID, limit, offset, enrich)
	case errors.Is(err, mongo.ErrNoDocuments):
		return s.getRoomSubscriptions(ctx, roomID, limit, offset, enrich)
	default:
		return nil, fmt.Errorf("probe room_members for %q: %w", roomID, err)
	}
}

func (s *MongoStore) getRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.M{"rid": roomID}}},
		bson.D{{Key: "$addFields", Value: bson.M{
			"typeOrder": bson.M{"$cond": bson.A{
				bson.M{"$eq": bson.A{"$member.type", "org"}}, 0, 1,
			}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{
			{Key: "typeOrder", Value: 1},
			{Key: "ts", Value: 1},
			{Key: "_id", Value: 1},
		}}},
	}
	if offset != nil && *offset > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$skip", Value: int64(*offset)}})
	}
	// Mongo rejects {$limit: 0}; guard here too for direct internal callers.
	if limit != nil && *limit > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$limit", Value: int64(*limit)}})
	}

	if enrich {
		pipeline = append(pipeline, enrichRoomMembersStages(roomID)...)
	}

	// Drop the helper typeOrder field last so it never leaks into the result.
	pipeline = append(pipeline, bson.D{{Key: "$project", Value: bson.M{"typeOrder": 0}}})

	cursor, err := s.roomMembers.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate room_members for %q: %w", roomID, err)
	}
	defer cursor.Close(ctx)

	if !enrich {
		members := []model.RoomMember{}
		if err := cursor.All(ctx, &members); err != nil {
			return nil, fmt.Errorf("decode room_members for %q: %w", roomID, err)
		}
		return members, nil
	}

	// Enriched path: decode into a hybrid row that carries a `display` sub-doc (sidesteps
	// the bson:"-" tags on RoomMemberEntry display fields), then copy values to Member.*.
	var rows []roomMemberEnrichedRow
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode enriched room_members for %q: %w", roomID, err)
	}
	members := make([]model.RoomMember, 0, len(rows))
	for i := range rows {
		rm := rows[i].RoomMember
		d := rows[i].Display
		rm.Member.EngName = d.EngName
		rm.Member.ChineseName = d.ChineseName
		rm.Member.IsOwner = d.IsOwner
		rm.Member.MemberCount = d.MemberCount
		// Two-pass tiebreak: dept names when non-empty, else sect; CombineWithFallback falls back to member.id.
		// Must be byte-identical to room-worker's processRemoveOrg/displayOrg logic.
		if rm.Member.Type == model.RoomMemberOrg {
			var name, tcName string
			if d.OrgRaw != nil {
				if d.OrgRaw.IsDept && (d.OrgRaw.DeptName != "" || d.OrgRaw.DeptTCName != "") {
					name, tcName = d.OrgRaw.DeptName, d.OrgRaw.DeptTCName
				}
				if name == "" && tcName == "" {
					name, tcName = d.OrgRaw.SectName, d.OrgRaw.SectTCName
				}
			}
			rm.Member.OrgName = displayfmt.CombineWithFallback(name, tcName, rm.Member.ID)
		}
		members = append(members, rm)
	}
	return members, nil
}

// roomMemberEnrichedRow is the decode target for the enriched aggregation pipeline.
// The `display` sub-doc sidesteps the bson:"-" tags on RoomMemberEntry display fields.
type roomMemberEnrichedRow struct {
	model.RoomMember `bson:",inline"`
	Display          roomMemberEnrichedDisplay `bson:"display"`
}

type roomMemberEnrichedDisplay struct {
	EngName     string         `bson:"engName,omitempty"`
	ChineseName string         `bson:"chineseName,omitempty"`
	IsOwner     bool           `bson:"isOwner,omitempty"`
	MemberCount int            `bson:"memberCount,omitempty"`
	OrgRaw      *orgRawDisplay `bson:"orgRaw,omitempty"`
}

// orgRawDisplay carries the raw org-lookup result for Go-side dept-vs-sect decision via
// displayfmt.CombineWithFallback. Nil means no user matched the org id; loop uses member.id.
type orgRawDisplay struct {
	IsDept     bool   `bson:"isDept,omitempty"`
	DeptName   string `bson:"deptName,omitempty"`
	DeptTCName string `bson:"deptTCName,omitempty"`
	SectName   string `bson:"sectName,omitempty"`
	SectTCName string `bson:"sectTCName,omitempty"`
}

// enrichRoomMembersStages returns $lookup+$set stages for enrich=true.
// Output goes into a `display` sub-doc to survive RoomMemberEntry bson:"-" tags.
func enrichRoomMembersStages(roomID string) []bson.D {
	return []bson.D{
		// Individuals: join users on account → pull engName / chineseName.
		{{Key: "$lookup", Value: bson.M{
			"from": "users",
			"let": bson.M{
				"acct": "$member.account",
				"mtyp": "$member.type",
			},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$$mtyp", "individual"}},
					bson.M{"$eq": bson.A{"$account", "$$acct"}},
				}}}},
				bson.M{"$limit": 1},
				bson.M{"$project": bson.M{"engName": 1, "chineseName": 1, "_id": 0}},
			},
			"as": "_userMatch",
		}}},
		// Individuals: join subscriptions on (roomId, u.account) → pull roles.
		{{Key: "$lookup", Value: bson.M{
			"from": "subscriptions",
			"let": bson.M{
				"acct": "$member.account",
				"mtyp": "$member.type",
			},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$$mtyp", "individual"}},
					bson.M{"$eq": bson.A{"$roomId", roomID}},
					bson.M{"$eq": bson.A{"$u.account", "$$acct"}},
				}}}},
				bson.M{"$limit": 1},
				bson.M{"$project": bson.M{"roles": 1, "_id": 0}},
			},
			"as": "_subMatch",
		}}},
		// Orgs: join users by deptId OR sectId → one grouped doc with isDept/name fields.
		// Dept-vs-sect decision and name combine happen Go-side via displayfmt.CombineWithFallback.
		{{Key: "$lookup", Value: bson.M{
			"from": "users",
			"let": bson.M{
				"orgId": "$member.id",
				"mtyp":  "$member.type",
			},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$$mtyp", "org"}},
					bson.M{"$or": bson.A{
						bson.M{"$eq": bson.A{"$deptId", "$$orgId"}},
						bson.M{"$eq": bson.A{"$sectId", "$$orgId"}},
					}},
				}}}},
				bson.M{"$addFields": bson.M{
					"_isDept": bson.M{"$eq": bson.A{"$deptId", "$$orgId"}},
					"_name":   bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$deptId", "$$orgId"}}, "$deptName", "$sectName"}},
					"_tcName": bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$deptId", "$$orgId"}}, "$deptTCName", "$sectTCName"}},
				}},
				// $max over bool: "any user matched deptId" wins over all sect-only matches.
				// Name fields are gated by _isDept so only the winning branch's strings flow through.
				bson.M{"$group": bson.M{
					"_id":         nil,
					"isDept":      bson.M{"$max": "$_isDept"},
					"deptName":    bson.M{"$max": bson.M{"$cond": bson.A{"$_isDept", "$_name", nil}}},
					"deptTCName":  bson.M{"$max": bson.M{"$cond": bson.A{"$_isDept", "$_tcName", nil}}},
					"sectName":    bson.M{"$max": bson.M{"$cond": bson.A{"$_isDept", nil, "$_name"}}},
					"sectTCName":  bson.M{"$max": bson.M{"$cond": bson.A{"$_isDept", nil, "$_tcName"}}},
					"memberCount": bson.M{"$sum": 1},
				}},
			},
			"as": "_orgMatch",
		}}},
		// Fold the three matches into a `display` sub-doc; orgRaw is nil when no users matched.
		{{Key: "$set", Value: bson.M{
			"display": bson.M{
				"engName":     bson.M{"$arrayElemAt": bson.A{"$_userMatch.engName", 0}},
				"chineseName": bson.M{"$arrayElemAt": bson.A{"$_userMatch.chineseName", 0}},
				"isOwner": bson.M{"$in": bson.A{
					"owner",
					bson.M{"$ifNull": bson.A{
						bson.M{"$arrayElemAt": bson.A{"$_subMatch.roles", 0}},
						bson.A{},
					}},
				}},
				"orgRaw":      bson.M{"$arrayElemAt": bson.A{"$_orgMatch", 0}},
				"memberCount": bson.M{"$arrayElemAt": bson.A{"$_orgMatch.memberCount", 0}},
			},
		}}},
		// Drop the temporary join arrays.
		{{Key: "$project", Value: bson.M{"_userMatch": 0, "_subMatch": 0, "_orgMatch": 0}}},
	}
}

func (s *MongoStore) getRoomSubscriptions(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
	opts := options.Find().SetSort(bson.D{
		{Key: "joinedAt", Value: 1},
		{Key: "_id", Value: 1},
	})
	if offset != nil && *offset > 0 {
		opts.SetSkip(int64(*offset))
	}
	// SetLimit(0) means "no limit" in the driver, which would silently return
	// unbounded results. Only set when >0 so it matches the aggregation path.
	if limit != nil && *limit > 0 {
		opts.SetLimit(int64(*limit))
	}
	cursor, err := s.subscriptions.Find(ctx, bson.M{"roomId": roomID}, opts)
	if err != nil {
		return nil, fmt.Errorf("find subscriptions for %q: %w", roomID, err)
	}
	defer cursor.Close(ctx)

	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, fmt.Errorf("decode subscriptions for %q: %w", roomID, err)
	}

	members := make([]model.RoomMember, 0, len(subs))
	for i := range subs {
		sub := &subs[i]
		entry := model.RoomMemberEntry{
			ID:      sub.User.ID,
			Type:    model.RoomMemberIndividual,
			Account: sub.User.Account,
		}
		if enrich {
			entry.IsOwner = hasRole(sub.Roles, model.RoleOwner)
		}
		members = append(members, model.RoomMember{
			ID:     sub.ID,
			RoomID: roomID,
			Ts:     sub.JoinedAt,
			Member: entry,
		})
	}

	if enrich && len(members) > 0 {
		if err := s.attachUserDisplayNames(ctx, roomID, members); err != nil {
			return nil, fmt.Errorf("attach user display names for %q: %w", roomID, err)
		}
	}
	return members, nil
}

// attachUserDisplayNames batch-loads display fields for individual members in place.
// Human accounts → users (EngName/ChineseName); bot accounts → apps (Name). Each partition queried only when non-empty.
func (s *MongoStore) attachUserDisplayNames(ctx context.Context, roomID string, members []model.RoomMember) error {
	var humanAccounts, botAccounts []string
	for i := range members {
		if members[i].Member.Type != model.RoomMemberIndividual || members[i].Member.Account == "" {
			continue
		}
		if botAccountPattern.MatchString(members[i].Member.Account) {
			botAccounts = append(botAccounts, members[i].Member.Account)
		} else {
			humanAccounts = append(humanAccounts, members[i].Member.Account)
		}
	}

	var (
		userByAccount  map[string]*model.User
		appByAssistant map[string]string // assistant.name → app.name
	)
	if len(humanAccounts) > 0 {
		u, err := s.findUsersForDisplay(ctx, humanAccounts)
		if err != nil {
			return fmt.Errorf("find users for room %q: %w", roomID, err)
		}
		userByAccount = u
	}
	if len(botAccounts) > 0 {
		a, err := s.findAppsForDisplay(ctx, botAccounts)
		if err != nil {
			return fmt.Errorf("find apps for room %q: %w", roomID, err)
		}
		appByAssistant = a
	}

	for i := range members {
		if members[i].Member.Type != model.RoomMemberIndividual {
			continue
		}
		acct := members[i].Member.Account
		if u, ok := userByAccount[acct]; ok {
			members[i].Member.EngName = u.EngName
			members[i].Member.ChineseName = u.ChineseName
			continue
		}
		if name, ok := appByAssistant[acct]; ok {
			members[i].Member.Name = name
		}
	}
	return nil
}

// findUsersForDisplay returns engName/chineseName indexed by account; users.account index covers the $in filter.
func (s *MongoStore) findUsersForDisplay(ctx context.Context, accounts []string) (map[string]*model.User, error) {
	cursor, err := s.users.Find(ctx,
		bson.M{"account": bson.M{"$in": accounts}},
		options.Find().SetProjection(bson.M{"_id": 0, "account": 1, "engName": 1, "chineseName": 1}),
	)
	if err != nil {
		return nil, fmt.Errorf("find users for display: %w", err)
	}
	defer cursor.Close(ctx)

	var users []model.User
	if err := cursor.All(ctx, &users); err != nil {
		return nil, fmt.Errorf("decode users for display: %w", err)
	}
	out := make(map[string]*model.User, len(users))
	for i := range users {
		out[users[i].Account] = &users[i]
	}
	return out, nil
}

// findAppsForDisplay returns app.name indexed by assistant.name; apps (assistant.name) index covers the $in filter.
func (s *MongoStore) findAppsForDisplay(ctx context.Context, botAccounts []string) (map[string]string, error) {
	cursor, err := s.apps.Find(ctx,
		bson.M{"assistant.name": bson.M{"$in": botAccounts}},
		options.Find().SetProjection(bson.M{"_id": 0, "name": 1, "assistant.name": 1}),
	)
	if err != nil {
		return nil, fmt.Errorf("find apps for display: %w", err)
	}
	defer cursor.Close(ctx)

	type row struct {
		Name      string `bson:"name"`
		Assistant struct {
			Name string `bson:"name"`
		} `bson:"assistant"`
	}
	var rows []row
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode apps for display: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Assistant.Name] = r.Name
	}
	return out, nil
}

func (s *MongoStore) GetUser(ctx context.Context, account string) (*model.User, error) {
	var u model.User
	err := s.users.FindOne(ctx, bson.M{"account": account}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user %q: %w", account, err)
	}
	return &u, nil
}

func (s *MongoStore) GetApp(ctx context.Context, botAccount string) (*model.App, error) {
	var a model.App
	err := s.apps.FindOne(ctx, bson.M{"assistant.name": botAccount}).Decode(&a)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrAppNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get app for bot %q: %w", botAccount, err)
	}
	return &a, nil
}

func (s *MongoStore) FindDMSubscription(ctx context.Context, account, targetName string) (*model.Subscription, error) {
	var sub model.Subscription
	err := s.subscriptions.FindOne(ctx, bson.M{
		"u.account": account,
		"name":      targetName,
		"roomType":  bson.M{"$in": []model.RoomType{model.RoomTypeDM, model.RoomTypeBotDM}},
	}).Decode(&sub)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, model.ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find dm subscription: %w", err)
	}
	return &sub, nil
}

// ListOrgMembers returns users matching orgID by sectId OR deptId, sorted by account asc.
// Both compound indexes cover the $or; returns RoomInvalidOrg when no users match.
func (s *MongoStore) ListOrgMembers(ctx context.Context, orgID string) ([]model.OrgMember, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "account", Value: 1}}).
		SetProjection(bson.M{
			"_id":         1,
			"account":     1,
			"engName":     1,
			"chineseName": 1,
			"siteId":      1,
		})
	cursor, err := s.users.Find(ctx, bson.M{"$or": []bson.M{
		{"sectId": orgID},
		{"deptId": orgID},
	}}, opts)
	if err != nil {
		return nil, fmt.Errorf("find users for org %q: %w", orgID, err)
	}
	defer cursor.Close(ctx)

	var members []model.OrgMember
	if err := cursor.All(ctx, &members); err != nil {
		return nil, fmt.Errorf("decode users for org %q: %w", orgID, err)
	}
	if len(members) == 0 {
		return nil, errcode.BadRequest(fmt.Sprintf("list org members for %q", orgID), errcode.WithReason(errcode.RoomInvalidOrg))
	}
	return members, nil
}

// FindExistingOrgIDs returns the subset of orgIDs matched by at least one user via sectId or deptId.
// Two Distinct calls (one per indexed field) beat $unionWith here: simpler, fewer allocations, same index coverage.
func (s *MongoStore) FindExistingOrgIDs(ctx context.Context, orgIDs []string) ([]string, error) {
	if len(orgIDs) == 0 {
		return nil, nil
	}
	var sectIDs []string
	if err := s.users.Distinct(ctx, "sectId", bson.M{"sectId": bson.M{"$in": orgIDs}}).Decode(&sectIDs); err != nil {
		return nil, fmt.Errorf("distinct sectIds for org validation: %w", err)
	}
	var deptIDs []string
	if err := s.users.Distinct(ctx, "deptId", bson.M{"deptId": bson.M{"$in": orgIDs}}).Decode(&deptIDs); err != nil {
		return nil, fmt.Errorf("distinct deptIds for org validation: %w", err)
	}
	out := make([]string, 0, len(sectIDs)+len(deptIDs))
	seen := make(map[string]struct{}, len(sectIDs)+len(deptIDs))
	for _, id := range sectIDs {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	for _, id := range deptIDs {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out, nil
}

// FindExistingAccounts returns the subset of accounts with a matching user document.
func (s *MongoStore) FindExistingAccounts(ctx context.Context, accounts []string) ([]string, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	var out []string
	if err := s.users.Distinct(ctx, "account", bson.M{"account": bson.M{"$in": accounts}}).Decode(&out); err != nil {
		return nil, fmt.Errorf("distinct accounts for user validation: %w", err)
	}
	return out, nil
}

// UpdateSubscriptionRead sets lastSeenAt and alert; returns ErrSubscriptionNotFound when no row matches.
func (s *MongoStore) UpdateSubscriptionRead(ctx context.Context, roomID, account string, lastSeenAt time.Time, alert bool) error {
	res, err := s.subscriptions.UpdateOne(ctx,
		bson.M{"roomId": roomID, "u.account": account},
		bson.M{"$set": bson.M{"lastSeenAt": lastSeenAt, "alert": alert}},
	)
	if err != nil {
		return fmt.Errorf("update subscription read for %q in room %q: %w", account, roomID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("update subscription read for %q in room %q: %w", account, roomID, model.ErrSubscriptionNotFound)
	}
	return nil
}

// findOneAndUpdateSub applies a pipeline $set to the subscription keyed by (roomID, account)
// and returns the post-update document; ErrNoDocuments maps to ErrSubscriptionNotFound.
func (s *MongoStore) findOneAndUpdateSub(ctx context.Context, roomID, account, op string, set bson.M) (*model.Subscription, error) {
	filter := bson.M{"roomId": roomID, "u.account": account}
	update := mongo.Pipeline{bson.D{{Key: "$set", Value: set}}}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	var result model.Subscription
	if err := s.subscriptions.FindOneAndUpdate(ctx, filter, update, opts).Decode(&result); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("%s for %q in room %q: %w", op, account, roomID, model.ErrSubscriptionNotFound)
		}
		return nil, fmt.Errorf("%s for %q in room %q: %w", op, account, roomID, err)
	}
	return &result, nil
}

// ToggleSubscriptionMute flips muted. $ifNull treats an absent field as false so
// legacy docs toggle to true on first call.
func (s *MongoStore) ToggleSubscriptionMute(ctx context.Context, roomID, account string) (*model.Subscription, error) {
	return s.findOneAndUpdateSub(ctx, roomID, account, "toggle mute", bson.M{
		"muted": bson.M{"$not": bson.A{bson.M{"$ifNull": bson.A{"$muted", false}}}},
	})
}

// ToggleSubscriptionFavorite flips favorite. $ifNull treats an absent field as
// false so legacy docs toggle to true on first call.
func (s *MongoStore) ToggleSubscriptionFavorite(ctx context.Context, roomID, account string) (*model.Subscription, error) {
	return s.findOneAndUpdateSub(ctx, roomID, account, "toggle favorite", bson.M{
		"favorite": bson.M{"$not": bson.A{bson.M{"$ifNull": bson.A{"$favorite", false}}}},
	})
}

// SetOwnerRole atomically grants or revokes the owner role; promote appends "owner" only when absent,
// demote filters it out preserving other roles and stable order.
func (s *MongoStore) SetOwnerRole(ctx context.Context, roomID, account string, makeOwner bool) (*model.Subscription, error) {
	currentRoles := bson.M{"$ifNull": bson.A{"$roles", bson.A{}}}
	var rolesExpr bson.M
	if makeOwner {
		rolesExpr = bson.M{"$cond": bson.M{
			"if":   bson.M{"$in": bson.A{model.RoleOwner, currentRoles}},
			"then": currentRoles,
			"else": bson.M{"$concatArrays": bson.A{currentRoles, bson.A{model.RoleOwner}}},
		}}
	} else {
		// Remove owner and ensure member is present: a creator seeded with ["owner"] only demotes
		// to ["member"], never []. Mirrors the worker's AddRole(member)-before-RemoveRole(owner) guard.
		withoutOwner := bson.M{"$filter": bson.M{
			"input": currentRoles,
			"cond":  bson.M{"$ne": bson.A{"$$this", model.RoleOwner}},
		}}
		rolesExpr = bson.M{"$cond": bson.M{
			"if":   bson.M{"$in": bson.A{model.RoleMember, withoutOwner}},
			"then": withoutOwner,
			"else": bson.M{"$concatArrays": bson.A{withoutOwner, bson.A{model.RoleMember}}},
		}}
	}
	return s.findOneAndUpdateSub(ctx, roomID, account, "set owner role", bson.M{"roles": rolesExpr})
}

// GetUserSiteID looks up users.siteId by account. Returns ("", nil) if no
// user document exists.
func (s *MongoStore) GetUserSiteID(ctx context.Context, account string) (string, error) {
	var doc struct {
		SiteID string `bson:"siteId"`
	}
	err := s.users.FindOne(ctx, bson.M{"account": account},
		options.FindOne().SetProjection(bson.M{"siteId": 1, "_id": 0})).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", nil
		}
		return "", fmt.Errorf("get user siteId for %q: %w", account, err)
	}
	return doc.SiteID, nil
}

// MinSubscriptionLastSeenByRoomID returns the minimum lastSeenAt across all subscriptions,
// or nil if any sub has a missing/null/zero lastSeenAt or the room has no subscriptions.
func (s *MongoStore) MinSubscriptionLastSeenByRoomID(ctx context.Context, roomID string) (*time.Time, error) {
	// Single covered index seek: BSON sorts missing/null before zero before real dates, so the
	// first doc by asc lastSeenAt answers both: missing/null/zero → nil; real date → minimum.
	var doc struct {
		LastSeenAt time.Time `bson:"lastSeenAt"`
	}
	err := s.subscriptions.FindOne(ctx,
		bson.M{"roomId": roomID},
		options.FindOne().
			SetSort(bson.D{{Key: "lastSeenAt", Value: 1}}).
			SetProjection(bson.M{"lastSeenAt": 1, "_id": 0}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil // no subscriptions in the room
	}
	if err != nil {
		return nil, fmt.Errorf("find min lastSeenAt for room %q: %w", roomID, err)
	}
	// missing/null/zero all decode to zero time → "never read".
	if !doc.LastSeenAt.After(time.Time{}) {
		return nil, nil
	}
	minTime := doc.LastSeenAt
	return &minTime, nil
}

// UpdateRoomMinUserLastSeenAt sets or clears rooms.minUserLastSeenAt for roomID.
func (s *MongoStore) UpdateRoomMinUserLastSeenAt(ctx context.Context, roomID string, t *time.Time) error {
	var update bson.M
	if t == nil {
		update = bson.M{"$unset": bson.M{"minUserLastSeenAt": ""}}
	} else {
		update = bson.M{"$set": bson.M{"minUserLastSeenAt": *t}}
	}
	if _, err := s.rooms.UpdateOne(ctx, bson.M{"_id": roomID}, update); err != nil {
		return fmt.Errorf("update minUserLastSeenAt for room %q: %w", roomID, err)
	}
	return nil
}

func (s *MongoStore) ListReadReceipts(
	ctx context.Context,
	roomID string,
	since time.Time,
	excludeAccount string,
	limit int,
) ([]ReadReceiptRow, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"roomId":     roomID,
			"lastSeenAt": bson.M{"$gte": since},
			"u.account":  bson.M{"$ne": excludeAccount},
		}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "users",
			"let":  bson.M{"uid": "$u._id"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$eq": []any{"$_id", "$$uid"}}}},
				bson.M{"$project": bson.M{"_id": 1, "account": 1, "chineseName": 1, "engName": 1}},
			},
			"as": "user",
		}}},
		{{Key: "$unwind", Value: bson.M{
			"path":                       "$user",
			"preserveNullAndEmptyArrays": false,
		}}},
		{{Key: "$replaceWith", Value: "$user"}},
		{{Key: "$limit", Value: int64(limit)}},
	}
	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate read receipts for room %q: %w", roomID, err)
	}
	defer cursor.Close(ctx)

	rows := make([]ReadReceiptRow, 0)
	for cursor.Next(ctx) {
		var r ReadReceiptRow
		if err := cursor.Decode(&r); err != nil {
			return nil, fmt.Errorf("decode read-receipt row for room %q: %w", roomID, err)
		}
		rows = append(rows, r)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate read receipts for room %q: %w", roomID, err)
	}
	return rows, nil
}

func (s *MongoStore) GetThreadSubscriptionByParent(ctx context.Context, account, parentMessageID, roomID string) (*model.ThreadSubscription, error) {
	var ts model.ThreadSubscription
	err := s.threadSubscriptions.FindOne(ctx, bson.M{
		"parentMessageId": parentMessageID,
		"userAccount":     account,
		"roomId":          roomID,
	}).Decode(&ts)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("find thread subscription for %q parent %q in room %q: %w",
				account, parentMessageID, roomID, model.ErrThreadSubscriptionNotFound)
		}
		return nil, fmt.Errorf("find thread subscription for %q parent %q in room %q: %w",
			account, parentMessageID, roomID, err)
	}
	return &ts, nil
}

// UpdateSubscriptionThreadRead removes threadID from threadUnread; clears alert and $unsets
// the field when threadUnread becomes empty.
func (s *MongoStore) UpdateSubscriptionThreadRead(ctx context.Context, roomID, account, threadID string) ([]string, bool, error) {
	filter := bson.M{"roomId": roomID, "u.account": account}

	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var updated model.Subscription
	err := s.subscriptions.FindOneAndUpdate(ctx, filter,
		bson.M{"$pull": bson.M{"threadUnread": threadID}},
		opts,
	).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, false, fmt.Errorf("update subscription thread-read for %q in room %q: %w",
			account, roomID, model.ErrSubscriptionNotFound)
	}
	if err != nil {
		return nil, false, fmt.Errorf("update subscription thread-read for %q in room %q: %w", account, roomID, err)
	}

	if len(updated.ThreadUnread) == 0 {
		if _, err = s.subscriptions.UpdateOne(ctx, filter, bson.M{
			"$set":   bson.M{"alert": false},
			"$unset": bson.M{"threadUnread": ""},
		}); err != nil {
			slog.WarnContext(ctx, "clear alert after empty threadUnread",
				"error", err, "account", account, "roomID", roomID)
		}
		return nil, false, nil
	}

	return updated.ThreadUnread, updated.Alert, nil
}

// ListDefaultChannelTabApps returns apps with channelTab.enabled=true AND channelTab.default=true,
// sorted by channelTab.name asc. Empty result is ([], nil).
func (s *MongoStore) ListDefaultChannelTabApps(ctx context.Context) ([]model.App, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "channelTab.name", Value: 1}}).
		SetProjection(bson.M{
			"_id":        1,
			"avatarUrl":  1,
			"assistant":  1,
			"channelTab": 1,
		})
	cursor, err := s.apps.Find(ctx, bson.M{
		"channelTab.enabled": true,
		"channelTab.default": true,
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("list default channel-tab apps: %w", err)
	}
	defer cursor.Close(ctx)
	apps := make([]model.App, 0, 8)
	if err := cursor.All(ctx, &apps); err != nil {
		return nil, fmt.Errorf("decode default channel-tab apps: %w", err)
	}
	return apps, nil
}

// ListRoomBotApps returns one entry per bot subscribed to roomID, joined to its app
// by assistant.name; only assistant.enabled=true rows are emitted, sorted by assistantName asc.
func (s *MongoStore) ListRoomBotApps(ctx context.Context, roomID string) ([]RoomBotAppEntry, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"roomId": roomID, "u.isBot": true}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "apps",
			"let":  bson.M{"acct": "$u.account"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
					bson.M{"$eq": bson.A{"$assistant.enabled", true}},
					bson.M{"$eq": bson.A{"$assistant.name", "$$acct"}},
				}}}},
				bson.M{"$project": bson.M{
					"_id":           0,
					"assistantName": "$assistant.name",
					"appName":       "$name",
				}},
			},
			"as": "app",
		}}},
		{{Key: "$unwind", Value: "$app"}},
		{{Key: "$replaceRoot", Value: bson.M{"newRoot": "$app"}}},
		{{Key: "$sort", Value: bson.D{{Key: "assistantName", Value: 1}}}},
	}
	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("list room bot apps for %q: %w", roomID, err)
	}
	defer cursor.Close(ctx)
	entries := make([]RoomBotAppEntry, 0, 4)
	if err := cursor.All(ctx, &entries); err != nil {
		return nil, fmt.Errorf("decode room bot apps for %q: %w", roomID, err)
	}
	return entries, nil
}

// ListActiveCmdMenus returns active bot_cmd_menu docs matching assistantNames, sorted by name asc.
// Returns ([], nil) when assistantNames is empty.
func (s *MongoStore) ListActiveCmdMenus(ctx context.Context, assistantNames []string) ([]model.BotCmdMenu, error) {
	if len(assistantNames) == 0 {
		return []model.BotCmdMenu{}, nil
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "name", Value: 1}}).
		SetProjection(bson.M{
			"_id":       0,
			"name":      1,
			"cmdBlocks": 1,
		})
	cursor, err := s.botCmdMenus.Find(ctx, bson.M{
		"activeStatus": true,
		"name":         bson.M{"$in": assistantNames},
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("list active cmd menus: %w", err)
	}
	defer cursor.Close(ctx)
	menus := make([]model.BotCmdMenu, 0, len(assistantNames))
	if err := cursor.All(ctx, &menus); err != nil {
		return nil, fmt.Errorf("decode active cmd menus: %w", err)
	}
	return menus, nil
}

// ListMemberStatuses returns up to limit live members projected from users; orphan subs (deleted user
// docs) are dropped by $unwind. $limit is post-join so orphan-prefix rows never under-deliver.
func (s *MongoStore) ListMemberStatuses(ctx context.Context, roomID string, limit int) ([]model.MemberStatus, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"roomId": roomID}}},
		// Join on u.account (not u._id as ListReadReceipts does); $limit 1 caps duplicate-account matches.
		{{Key: "$lookup", Value: bson.M{
			"from": "users",
			"let":  bson.M{"acct": "$u.account"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$eq": bson.A{"$account", "$$acct"}}}},
				bson.M{"$limit": 1},
				bson.M{"$project": bson.M{
					"_id":          0,
					"account":      1,
					"engName":      1,
					"chineseName":  1,
					"statusIsShow": 1,
					"statusText":   1,
				}},
			},
			"as": "user",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$user", "preserveNullAndEmptyArrays": false}}},
		{{Key: "$replaceWith", Value: "$user"}},
		{{Key: "$limit", Value: int64(limit)}},
	}
	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate member statuses for %q: %w", roomID, err)
	}
	defer cursor.Close(ctx)
	members := []model.MemberStatus{}
	if err := cursor.All(ctx, &members); err != nil {
		return nil, fmt.Errorf("decode member statuses for %q: %w", roomID, err)
	}
	return members, nil
}

// ListMentionableSubscriptions returns up to limit members matching escapedFilter (case-insensitive
// regex on dash-joined keyword). p_ accounts are excluded; .bot accounts emit optionType=app.
func (s *MongoStore) ListMentionableSubscriptions(
	ctx context.Context, roomID, excludeAccount, escapedFilter string, limit int,
) ([]model.MentionableSubscription, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"roomId": roomID,
			"u.account": bson.M{
				"$ne":  excludeAccount,
				"$not": bson.M{"$regex": platformAdminRegex},
			},
		}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "users",
			"let":  bson.M{"acct": "$u.account"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$eq": bson.A{"$account", "$$acct"}}}},
				bson.M{"$limit": 1},
				bson.M{"$project": bson.M{
					"_id": 0, "account": 1, "engName": 1, "chineseName": 1, "siteId": 1,
				}},
			},
			"as": "_users",
		}}},
		{{Key: "$lookup", Value: bson.M{
			"from": "apps",
			"let":  bson.M{"acct": "$u.account"},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$eq": bson.A{"$assistant.name", "$$acct"}}}},
				bson.M{"$limit": 1},
				bson.M{"$project": bson.M{
					"_id": 0, "name": 1, "assistant.name": 1,
				}},
			},
			"as": "_apps",
		}}},
		{{Key: "$addFields", Value: bson.M{
			"isApp":   bson.M{"$regexMatch": bson.M{"input": "$u.account", "regex": botAccountRegex}},
			"userDoc": bson.M{"$arrayElemAt": bson.A{"$_users", 0}},
			"appDoc":  bson.M{"$arrayElemAt": bson.A{"$_apps", 0}},
		}}},
		{{Key: "$addFields", Value: bson.M{
			"keyword": bson.M{"$concat": bson.A{
				bson.M{"$ifNull": bson.A{"$u.account", ""}}, "-",
				bson.M{"$ifNull": bson.A{"$userDoc.engName", ""}}, "-",
				bson.M{"$ifNull": bson.A{"$userDoc.chineseName", ""}}, "-",
				bson.M{"$ifNull": bson.A{"$appDoc.name", ""}}, "-",
				bson.M{"$ifNull": bson.A{"$appDoc.assistant.name", ""}},
			}},
		}}},
		{{Key: "$match", Value: bson.M{
			"keyword": bson.M{"$regex": escapedFilter, "$options": "i"},
		}}},
		{{Key: "$limit", Value: int64(limit)}},
		{{Key: "$project", Value: bson.M{
			"_id":        0,
			"optionType": bson.M{"$cond": bson.A{"$isApp", "app", "user"}},
			"userId":     "$u._id",
			"account":    "$u.account",
			"siteId": bson.M{"$cond": bson.A{
				"$isApp",
				"",
				bson.M{"$ifNull": bson.A{"$userDoc.siteId", ""}},
			}},
			"hrInfo": bson.M{"$cond": bson.A{
				"$isApp",
				"$$REMOVE",
				bson.M{
					"engName":     bson.M{"$ifNull": bson.A{"$userDoc.engName", ""}},
					"chineseName": bson.M{"$ifNull": bson.A{"$userDoc.chineseName", ""}},
				},
			}},
			"app": bson.M{"$cond": bson.A{
				"$isApp",
				bson.M{
					"name": bson.M{"$ifNull": bson.A{"$appDoc.name", ""}},
					"assistant": bson.M{
						"name": bson.M{"$ifNull": bson.A{"$appDoc.assistant.name", ""}},
					},
				},
				"$$REMOVE",
			}},
		}}},
	}

	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate mentionable subscriptions for %q: %w", roomID, err)
	}
	defer cursor.Close(ctx)
	subs := []model.MentionableSubscription{}
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, fmt.Errorf("decode mentionable subscriptions for %q: %w", roomID, err)
	}
	return subs, nil
}

// No order-safety guard on the source-site write; the $lt guard lives on the inbox-worker side.
func (s *MongoStore) UpdateThreadSubscriptionRead(ctx context.Context, threadRoomID, account string, lastSeenAt time.Time) error {
	filter := bson.M{"threadRoomId": threadRoomID, "userAccount": account}
	update := bson.M{"$set": bson.M{
		"lastSeenAt": lastSeenAt,
		"updatedAt":  lastSeenAt,
		"hasMention": false,
	}}
	res, err := s.threadSubscriptions.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update thread subscription read for %q in thread room %q: %w",
			account, threadRoomID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("update thread subscription read for %q in thread room %q: %w",
			account, threadRoomID, model.ErrThreadSubscriptionNotFound)
	}
	return nil
}

// UpdateRoomVisibility sets restricted, externalAccess, and updatedAt on the room.
func (s *MongoStore) UpdateRoomVisibility(ctx context.Context, roomID string, restricted, externalAccess bool) error {
	res, err := s.rooms.UpdateOne(ctx, bson.M{"_id": roomID}, bson.M{
		"$set": bson.M{
			"restricted":     restricted,
			"externalAccess": externalAccess,
			"updatedAt":      time.Now().UTC(),
		},
	})
	if err != nil {
		return fmt.Errorf("update room visibility %s: %w", roomID, err)
	}
	if res.MatchedCount == 0 {
		return ErrRoomNotFound
	}
	return nil
}

// ApplySubscriptionVisibility writes restricted/externalAccess to all subscriptions; when
// restricted=true+ownerAccount is set, atomically rewrites roles so only ownerAccount holds RoleOwner.
func (s *MongoStore) ApplySubscriptionVisibility(ctx context.Context, roomID string, restricted, externalAccess bool, ownerAccount string) error {
	filter := bson.M{"roomId": roomID}

	if restricted && ownerAccount != "" {
		// TOCTOU: owner could unsubscribe between this count and UpdateMany → zero owners; acceptable for rare admin RPC.
		n, err := s.subscriptions.CountDocuments(ctx, bson.M{"roomId": roomID, "u.account": ownerAccount})
		if err != nil {
			return fmt.Errorf("count owner subscription: %w", err)
		}
		if n == 0 {
			return ErrOwnerNotSubscribed
		}
		pipeline := mongo.Pipeline{
			bson.D{{Key: "$set", Value: bson.M{
				"restricted":     true,
				"externalAccess": externalAccess,
				"roles": bson.M{"$cond": bson.M{
					"if":   bson.M{"$eq": bson.A{"$u.account", ownerAccount}},
					"then": bson.A{string(model.RoleOwner)},
					"else": bson.A{string(model.RoleMember)},
				}},
			}}},
		}
		if _, err := s.subscriptions.UpdateMany(ctx, filter, pipeline); err != nil {
			return fmt.Errorf("apply visibility (restrict+rewrite): %w", err)
		}
		return nil
	}

	if _, err := s.subscriptions.UpdateMany(ctx, filter, bson.M{
		"$set": bson.M{"restricted": restricted, "externalAccess": externalAccess},
	}); err != nil {
		return fmt.Errorf("apply visibility (flags only): %w", err)
	}
	return nil
}

// ListSubscriptionsByRoom returns every subscription in the room.
func (s *MongoStore) ListSubscriptionsByRoom(ctx context.Context, roomID string) ([]model.Subscription, error) {
	cursor, err := s.subscriptions.Find(ctx, bson.M{"roomId": roomID})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions for room %q: find: %w", roomID, err)
	}
	var subs []model.Subscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, fmt.Errorf("list subscriptions for room %q: decode: %w", roomID, err)
	}
	return subs, nil
}

// FindUsersByAccounts returns User docs for the supplied accounts. Empty input
// returns nil, nil.
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
