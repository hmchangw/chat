package pipelines

import "go.mongodb.org/mongo-driver/v2/bson"

// ActiveSubscriptionFilter is the clause that excludes soft-deleted
// (tombstoned) subscriptions from a plain find/aggregation $match. A missing
// `deleted` field counts as active, so the clause is `deleted != true`.
func ActiveSubscriptionFilter() bson.M { return bson.M{"deleted": bson.M{"$ne": true}} }

// ActiveSubscriptionExpr is the aggregation-$expr equivalent of
// ActiveSubscriptionFilter, for use inside a $lookup inner pipeline's
// `$expr: {$and: [...]}`. Resolves to `{$ne: ["$deleted", true]}`.
func ActiveSubscriptionExpr() bson.M { return bson.M{"$ne": bson.A{"$deleted", true}} }
