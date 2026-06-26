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
// $lookup justification: three joins, none avoidable.
//  1. subscriptions (membership) runs FIRST, on the thread_subscription's own
//     roomId — the room subscription, not the thread subscription, is the source
//     of truth for whether the user still belongs to the room (purged on leave;
//     thread_subscriptions rows are not). Filtering here, before $limit, keeps the
//     page exact; doing it before the thread_rooms join means that join runs only
//     for accessible threads. Indexed point read on (u.account, roomId).
//  2. thread_rooms supplies the inbox sort key (lastMsgAt) and parent/activity
//     fields, which live there rather than on thread_subscriptions, so we must
//     sort and paginate on the looked-up field. Denormalizing lastMsgAt onto every
//     subscription was rejected because it would write-amplify across all
//     subscribers on every reply (see docs/design/user-thread-list.md §5).
//  3. rooms (name/type) runs AFTER $limit, so it enriches only the ≤limit+1 page
//     rows with indexed _id point reads — cheaper than a separate round trip, and
//     it avoids bolting a non-cacheable GetRoomsMeta onto the cached RoomRepository.
func userThreadSubscriptionsPipeline(account string, cursorLastMsgAt *time.Time, cursorThreadRoomID string, limit int) bson.A {
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: bson.M{"userAccount": account}}},
		// Membership filter (applied first): keep only threads whose room the user
		// is still subscribed to, keyed on the thread_subscription's own roomId so
		// the thread_rooms/rooms joins below run only for accessible threads. The
		// {$ne: []} existence match drops threads in rooms the user has left
		// (their subscription is purged on leave; the thread_subscriptions row is
		// not). Indexed point read on (u.account, roomId).
		bson.D{{Key: "$lookup", Value: bson.M{
			"from": subscriptionsCollection,
			"let":  bson.M{"rid": "$roomId"},
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
		bson.D{{Key: "$match", Value: bson.M{"sub": bson.M{"$ne": bson.A{}}}}},
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
	pipeline = append(pipeline,
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
