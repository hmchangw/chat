//go:build e2e

package harness

import (
	"testing"

	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMustSeed_ReturnsSeedFromKeypair(t *testing.T) {
	kp, err := nkeys.CreateUser()
	require.NoError(t, err)
	seed := mustSeed(t, kp)
	assert.NotEmpty(t, seed)
	assert.Equal(t, byte('S'), seed[0], "nkey user seeds always start with S")
	assert.Equal(t, byte('U'), seed[1], "user seeds carry the U role byte at index 1")
}
