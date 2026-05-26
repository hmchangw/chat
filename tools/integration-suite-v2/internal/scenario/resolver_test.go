package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/fixtures"
)

func TestResolve_PicksFirstMatchingFixture(t *testing.T) {
	cast := &fixtures.Cast{
		Users: []fixtures.CastUser{
			{ID: "alice", Tags: []string{"verified"}, Account: "alice"},
			{ID: "bob", Tags: []string{"verified"}, Account: "bob"},
			{ID: "dave", Tags: []string{"unverified"}, Account: "dave"},
		},
	}
	s := &Scenario{
		Input: Input{
			Placeholders: map[string]Placeholder{
				"requester": {Type: "user", Predicate: map[string]any{"verified": true}},
			},
		},
	}
	res, err := Resolve(s, cast)
	require.NoError(t, err)
	assert.Equal(t, "alice", res.Users["requester"].ID)
}

func TestResolve_DistinctPicksAcrossPlaceholders(t *testing.T) {
	cast := &fixtures.Cast{
		Users: []fixtures.CastUser{
			{ID: "alice", Tags: []string{"verified"}, Account: "alice"},
			{ID: "bob", Tags: []string{"verified"}, Account: "bob"},
		},
	}
	s := &Scenario{
		Input: Input{
			Placeholders: map[string]Placeholder{
				"requester": {Type: "user", Predicate: map[string]any{"verified": true}},
				"other":     {Type: "user", Predicate: map[string]any{"verified": true}},
			},
		},
	}
	res, err := Resolve(s, cast)
	require.NoError(t, err)
	assert.NotEqual(t, res.Users["requester"].Account, res.Users["other"].Account,
		"two placeholders with the same predicate must pick distinct fixtures")
}

func TestResolve_DeterministicOrder(t *testing.T) {
	// Sorted placeholder names: "other" < "requester" — so "other" picks first (alice).
	cast := &fixtures.Cast{
		Users: []fixtures.CastUser{
			{ID: "alice", Tags: []string{"verified"}, Account: "alice"},
			{ID: "bob", Tags: []string{"verified"}, Account: "bob"},
		},
	}
	s := &Scenario{
		Input: Input{
			Placeholders: map[string]Placeholder{
				"requester": {Type: "user", Predicate: map[string]any{"verified": true}},
				"other":     {Type: "user", Predicate: map[string]any{"verified": true}},
			},
		},
	}
	// Run multiple times; same picks every time (no random map-iteration variance).
	for i := 0; i < 5; i++ {
		res, err := Resolve(s, cast)
		require.NoError(t, err)
		assert.Equal(t, "alice", res.Users["other"].Account, "iter %d", i)
		assert.Equal(t, "bob", res.Users["requester"].Account, "iter %d", i)
	}
}

func TestResolve_ExhaustedMatches_Fails(t *testing.T) {
	cast := &fixtures.Cast{
		Users: []fixtures.CastUser{
			{ID: "alice", Tags: []string{"verified"}, Account: "alice"},
		},
	}
	s := &Scenario{
		Input: Input{
			Placeholders: map[string]Placeholder{
				"first":  {Type: "user", Predicate: map[string]any{"verified": true}},
				"second": {Type: "user", Predicate: map[string]any{"verified": true}},
			},
		},
	}
	_, err := Resolve(s, cast)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already picked")
}

func TestResolve_NoMatchReturnsError(t *testing.T) {
	cast := &fixtures.Cast{Users: []fixtures.CastUser{{ID: "alice", Tags: []string{"verified"}}}}
	s := &Scenario{
		Input: Input{
			Placeholders: map[string]Placeholder{
				"requester": {Type: "user", Predicate: map[string]any{"banned": true}},
			},
		},
	}
	_, err := Resolve(s, cast)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no fixture matches")
}
