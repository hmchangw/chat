package mongorepo

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// buildBaseThreadMatch returns the base {roomId, threadParentCreatedAt?} filter
// shared by all three thread-room queries. accessSince restricts to threads
// whose parent was created at/after the user's join time.
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

// followingThreadsPipeline filters to threads where account is in replyAccounts.
func followingThreadsPipeline(roomID, account string, accessSince *time.Time) bson.A {
	match := buildBaseThreadMatch(roomID, accessSince)
	match["replyAccounts"] = account
	return bson.A{
		bson.D{{Key: "$match", Value: match}},
		bson.D{{Key: "$sort", Value: threadRoomSort}},
	}
}

// unreadThreadsPipeline returns threads that have unread activity for userAccount:
//  1. Match thread rooms (uses {roomId, lastMsgAt, threadParentCreatedAt} index).
//  2. $lookup threadSubscriptions for the requesting user.
//  3. Keep only threads where the user has a subscription.
//  4. Keep only threads where lastMsgAt > lastSeenAt (nil lastSeenAt counts as
//     never-seen, which is always less than lastMsgAt — so it's included as unread).
//  5. Drop the joined sub field before returning.
func unreadThreadsPipeline(roomID, userAccount string, accessSince *time.Time) bson.A {
	match := buildBaseThreadMatch(roomID, accessSince)
	return bson.A{
		bson.D{{Key: "$match", Value: match}},
		bson.D{{Key: "$lookup", Value: bson.M{
			"from": "threadSubscriptions",
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
