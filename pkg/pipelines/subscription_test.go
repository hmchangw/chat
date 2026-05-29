package pipelines

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestActiveSubscriptionFilter(t *testing.T) {
	got := ActiveSubscriptionFilter()
	assert.Equal(t, bson.M{"deleted": bson.M{"$ne": true}}, got)
}

func TestActiveSubscriptionExpr(t *testing.T) {
	got := ActiveSubscriptionExpr()
	assert.Equal(t, bson.M{"$ne": bson.A{"$deleted", true}}, got)
}
