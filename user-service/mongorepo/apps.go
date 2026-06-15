package mongorepo

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/user-service/models"
)

const appsCollection = "apps"

// AppRepo is the Mongo implementation of service.AppRepository.
type AppRepo struct {
	apps *mongoutil.Collection[model.App]
	// items views the same collection decoded as AppListItem ($addFields isSubscribed).
	items *mongoutil.Collection[models.AppListItem]
}

// NewAppRepo builds an AppRepo over db.
func NewAppRepo(db *mongo.Database) *AppRepo {
	col := db.Collection(appsCollection)
	return &AppRepo{
		apps:  mongoutil.NewCollection[model.App](col),
		items: mongoutil.NewCollection[models.AppListItem](col),
	}
}

// GetApp returns the app by id, or (nil, nil) when none matches.
func (r *AppRepo) GetApp(ctx context.Context, appID string) (*model.App, error) {
	return r.apps.FindByID(ctx, appID)
}

// ListApps returns the requested page of apps annotated with the requesting
// user's subscription flag (isSubscribed true when the user holds a subscribed
// botDM for the app's assistant). Results are sorted by name; Total is the full
// catalog count regardless of the page slice.
func (r *AppRepo) ListApps(ctx context.Context, account string, page mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[models.AppListItem], error) {
	pipeline := bson.A{
		bson.M{"$lookup": bson.M{
			"from": subscriptionsCollection,
			"let":  bson.M{"botName": "$assistant.name"},
			"pipeline": bson.A{bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
				// $literal so a $-prefixed account isn't read as a field path.
				bson.M{"$eq": bson.A{"$u.account", bson.M{"$literal": account}}},
				bson.M{"$eq": bson.A{"$name", "$$botName"}},
				bson.M{"$eq": bson.A{"$roomType", "botDM"}},
				bson.M{"$eq": bson.A{"$isSubscribed", true}},
			}}}}},
			"as": "sub",
		}},
		bson.M{"$addFields": bson.M{"isSubscribed": bson.M{"$gt": bson.A{bson.M{"$size": "$sub"}, 0}}}},
		bson.M{"$project": bson.M{"sub": 0}},
		bson.M{"$sort": bson.M{"name": 1}},
	}
	out, err := r.items.AggregatePaged(ctx, pipeline, page, mongoutil.WithAllowDiskUse())
	if err != nil {
		return mongoutil.OffsetPage[models.AppListItem]{}, fmt.Errorf("aggregate apps page: %w", err)
	}
	return out, nil
}
