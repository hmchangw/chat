package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlindspotFailure_NilWhenNoSlugs(t *testing.T) {
	assert.Nil(t, BlindspotFailure(nil))
	assert.Nil(t, BlindspotFailure([]string{}))
}

func TestBlindspotFailure_ContainsAllSlugs(t *testing.T) {
	err := BlindspotFailure([]string{"partial-history", "another"})
	require.NotNil(t, err)
	assert.Contains(t, err.Error(), "undocumented behavior")
	assert.Contains(t, err.Error(), "partial-history")
	assert.Contains(t, err.Error(), "another")
}
