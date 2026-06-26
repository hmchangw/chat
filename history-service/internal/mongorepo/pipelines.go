package mongorepo

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// accessSince gates to threads whose parent was created at or after the user's join time.
func buildBaseThreadMatch(roomID string, accessSince *time.Time) bson.M {
	match := bson.M{"roomId": roomID}
	if accessSince != nil {
		match["threadParentCreatedAt"] = bson.M{"$gte": *accessSince}
	}
	return match
}

func allThreadsPipeline(roomID string, accessSince *time.Time) bson.A {
	return bson.A{
		bson.D{{Key: "$match", Value: buildBaseThreadMatch(roomID, accessSince)}},
		bson.D{{Key: "$sort", Value: threadRoomSort}},
	}
}

func followingThreadsPipeline(roomID, account string, accessSince *time.Time) bson.A {
	match := buildBaseThreadMatch(roomID, accessSince)
	match["replyAccounts"] = account
	return bson.A{
		bson.D{{Key: "$match", Value: match}},
		bson.D{{Key: "$sort", Value: threadRoomSort}},
	}
}

// userThreadSubscriptionsPipeline lists one user's thread subscriptions, newest
// activity first, after the (lastMsgAt, threadRoomId) value cursor. It is driven
// from thread_subscriptions (the per-user filter) and joins thread_rooms for the
// activity/parent fields.
//
// $lookup justification: the inbox sort key (lastMsgAt) lives on thread_rooms,
// while the per-user filter (userAccount) lives on thread_subscriptions, so the
// thread_rooms join is unavoidable — we must sort and paginate on the looked-up
// field. Denormalizing lastMsgAt onto every subscription was rejected because it
// would write-amplify across all subscribers on every reply (see
// docs/design/user-thread-list.md §5). The second join (subscriptions) is the
// membership filter: the room subscription — not the thread subscription — is
// the source of truth for whether the user still belongs to the room (it is
// purged on leave; thread subscriptions are not), and that fact must be applied
// BEFORE $limit, so a correlated existence join on (u.account, roomId) is
// likewise unavoidable. The third join (rooms, for name/type) runs AFTER $limit,
// so it enriches only the ≤limit+1 rows of the page with indexed _id point reads
// — cheaper than a separate per-page round trip, and it avoids bolting a
// non-cacheable GetRoomsMeta onto the cached RoomRepository.
func userThreadSubscriptionsPipeline(account string, cursorLastMsgAt *time.Time, cursorThreadRoomID string, limit int) bson.A {
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: bson.M{"userAccount": account}}},
		bson.D{{Key: "$lookup", Value: bson.M{
			"from":         threadRoomsCollection,
			"localField":   "threadRoomId",
			"foreignField": "_id",
			"as":           "tr",
			"pipeline": bson.A{
				bson.D{{Key: "$project", Value: bson.M{
					"lastMsgAt": 1, "lastMsgId": 1, "parentMessageId": 1, "roomId": 1, "siteId": 1,
				}}},
			},
		}}},
		bson.D{{Key: "$unwind", Value: "$tr"}},
	}
	// Value cursor: items strictly older than the cursor in (lastMsgAt, threadRoomId) DESC order.
	if cursorLastMsgAt != nil {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: bson.M{
			"$or": bson.A{
				bson.M{"tr.lastMsgAt": bson.M{"$lt": *cursorLastMsgAt}},
				bson.M{"tr.lastMsgAt": *cursorLastMsgAt, "threadRoomId": bson.M{"$lt": cursorThreadRoomID}},
			},
		}}})
	}
	// Membership filter: keep only threads whose room the user is still
	// subscribed to. Runs BEFORE $sort/$limit so the page size and HasMore probe
	// stay exact (no post-fetch drops, no fill loop). The subscriptions index on
	// (u.account, roomId) makes each correlated lookup an indexed point read.
	pipeline = append(pipeline,
		bson.D{{Key: "$lookup", Value: bson.M{
			"from": subscriptionsCollection,
			"let":  bson.M{"rid": "$tr.roomId"},
			"pipeline": bson.A{
				bson.D{{Key: "$match", Value: bson.M{
					"u.account": account,
					"$expr":     bson.M{"$eq": bson.A{"$roomId", "$$rid"}},
				}}},
				bson.D{{Key: "$limit", Value: int64(1)}}, // existence only
				bson.D{{Key: "$project", Value: bson.M{"_id": 1}}},
			},
			"as": "sub",
		}}},
		// $lookup sets "sub" to [] when no subscription matched; non-empty ⇒ subscribed.
		bson.D{{Key: "$match", Value: bson.M{"sub": bson.M{"$ne": bson.A{}}}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "tr.lastMsgAt", Value: -1}, {Key: "threadRoomId", Value: -1}}}},
		bson.D{{Key: "$limit", Value: int64(limit + 1)}},
		// Page-scoped (post-$limit) room name/type enrichment; preserve rows whose
		// room doc is missing (degrade to empty name/type).
		bson.D{{Key: "$lookup", Value: bson.M{
			"from":         roomsCollection,
			"localField":   "tr.roomId",
			"foreignField": "_id",
			"as":           "room",
			"pipeline": bson.A{
				bson.D{{Key: "$project", Value: bson.M{"name": 1, "type": 1}}},
			},
		}}},
		bson.D{{Key: "$unwind", Value: bson.M{"path": "$room", "preserveNullAndEmptyArrays": true}}},
		bson.D{{Key: "$project", Value: bson.M{
			"_id":             "$threadRoomId",
			"roomId":          "$tr.roomId",
			"siteId":          "$tr.siteId",
			"roomName":        "$room.name",
			"roomType":        "$room.type",
			"parentMessageId": "$tr.parentMessageId",
			"lastMsgId":       "$tr.lastMsgId",
			"lastMsgAt":       "$tr.lastMsgAt",
			"lastSeenAt":      1,
			"hasMention":      1,
		}}},
	)
	return pipeline
}

// Unread = subscribed AND lastMsgAt > lastSeenAt (nil lastSeenAt = never seen = always unread).
func unreadThreadsPipeline(roomID, userAccount string, accessSince *time.Time) bson.A {
	match := buildBaseThreadMatch(roomID, accessSince)
	return bson.A{
		bson.D{{Key: "$match", Value: match}},
		bson.D{{Key: "$lookup", Value: bson.M{
			"from": "thread_subscriptions",
			"let":  bson.M{"tr": "$_id"},
			"pipeline": bson.A{
				bson.D{{Key: "$match", Value: bson.M{
					"$expr":       bson.M{"$eq": bson.A{"$threadRoomId", "$$tr"}},
					"userAccount": userAccount,
				}}},
				bson.D{{Key: "$project", Value: bson.M{"lastSeenAt": 1, "_id": 0}}},
			},
			"as": "sub",
		}}},
		// {$ne: []} — $lookup sets "sub" to [] when no subscription matched; non-empty means subscribed.
		bson.D{{Key: "$match", Value: bson.M{"sub": bson.M{"$ne": bson.A{}}}}},
		// null is the smallest BSON value, so $gt:[lastMsgAt, null] is true for any
		// non-null lastMsgAt — threads with nil lastSeenAt (never seen) are included.
		bson.D{{Key: "$match", Value: bson.M{
			"$expr": bson.M{"$gt": bson.A{"$lastMsgAt", bson.M{"$arrayElemAt": bson.A{"$sub.lastSeenAt", 0}}}},
		}}},
		bson.D{{Key: "$project", Value: bson.M{"sub": 0}}},
		bson.D{{Key: "$sort", Value: threadRoomSort}},
	}
}
