package fixtures

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCast_FindByPredicate(t *testing.T) {
	c := &Cast{
		Users: []CastUser{
			{ID: "alice", Tags: []string{"verified", "owner"}},
			{ID: "bob", Tags: []string{"verified"}},
			{ID: "carol", Tags: []string{"unverified"}},
		},
	}
	matched := c.FindByPredicate([]string{"verified"}, 0)
	assert.Len(t, matched, 2)
	assert.Equal(t, "alice", matched[0].ID)

	owners := c.FindByPredicate([]string{"verified", "owner"}, 0)
	assert.Len(t, owners, 1)
	assert.Equal(t, "alice", owners[0].ID)

	limited := c.FindByPredicate([]string{"verified"}, 1)
	assert.Len(t, limited, 1)

	none := c.FindByPredicate([]string{"nonexistent"}, 0)
	assert.Empty(t, none)
}
