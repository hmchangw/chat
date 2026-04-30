// Package pipelines holds shared MongoDB aggregation pipelines used by more
// than one service. Putting them here lets each service append its own
// terminal stage (e.g. $count vs. $group) without duplicating the leading
// stages.
package pipelines

import "go.mongodb.org/mongo-driver/v2/bson"

// GetNewMembersPipeline returns the common stages for finding the unique,
// non-bot, not-already-subscribed users that an add-members request would
// add to roomID, given org IDs and direct account names.
//
// Pipeline target: the users collection.
//
// Stages: $match (org/account filter, exclude bots), then (when roomID != "")
// $lookup + $match to filter out already-subscribed accounts. Empty roomID
// returns the $match stage only (used by capacity-check at create time).
//
// Callers MUST append a terminal stage that fits their need:
//   - room-service: bson.M{"$count": "n"}                                (capacity check)
//   - room-worker:  bson.M{"$group": {"_id": nil, "accounts": {"$addToSet": "$account"}}}
func GetNewMembersPipeline(orgIDs, directAccounts []string, roomID string) bson.A {
	orFilter := bson.A{}
	if len(orgIDs) > 0 {
		orFilter = append(orFilter, bson.M{"sectId": bson.M{"$in": orgIDs}})
	}
	if len(directAccounts) > 0 {
		orFilter = append(orFilter, bson.M{"account": bson.M{"$in": directAccounts}})
	}

	stages := bson.A{
		bson.M{"$match": bson.M{
			"$or":     orFilter,
			"account": bson.M{"$not": bson.Regex{Pattern: `(\.bot$|^p_)`, Options: ""}},
		}},
	}

	if roomID != "" {
		stages = append(stages,
			bson.M{"$lookup": bson.M{
				"from": "subscriptions",
				"let":  bson.M{"userAccount": "$account"},
				"pipeline": bson.A{
					bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
						bson.M{"$eq": bson.A{"$roomId", roomID}},
						bson.M{"$eq": bson.A{"$u.account", "$$userAccount"}},
					}}}},
					bson.M{"$limit": 1},
				},
				"as": "existingSub",
			}},
			bson.M{"$match": bson.M{"existingSub": bson.M{"$eq": bson.A{}}}},
		)
	}

	return stages
}
