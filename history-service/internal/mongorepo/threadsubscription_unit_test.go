package mongorepo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// countStages returns how many pipeline stages carry the given operator key.
func countStages(t *testing.T, pipeline bson.A, op string) int {
	t.Helper()
	n := 0
	for _, raw := range pipeline {
		stage, ok := raw.(bson.D)
		if !ok {
			continue
		}
		for _, e := range stage {
			if e.Key == op {
				n++
			}
		}
	}
	return n
}

func TestUserThreadSubscriptionsPipeline_FirstPageHasNoCursorMatch(t *testing.T) {
	p := userThreadSubscriptionsPipeline("alice", nil, "", 20)
	// First page: only the userAccount $match, no value-cursor $match.
	assert.Equal(t, 1, countStages(t, p, "$match"))
	// Two joins: thread_rooms (sort/cursor) and rooms (name/type), each unwound.
	assert.Equal(t, 2, countStages(t, p, "$lookup"))
	assert.Equal(t, 2, countStages(t, p, "$unwind"))
	assert.Equal(t, 1, countStages(t, p, "$sort"))
	assert.Equal(t, 1, countStages(t, p, "$limit"))
}

func TestUserThreadSubscriptionsPipeline_NextPageAddsCursorMatch(t *testing.T) {
	ts := time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)
	p := userThreadSubscriptionsPipeline("alice", &ts, "thr-9", 20)
	// Next page: userAccount $match + value-cursor $match.
	assert.Equal(t, 2, countStages(t, p, "$match"))
}
