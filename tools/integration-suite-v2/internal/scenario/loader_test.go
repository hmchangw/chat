package scenario

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadScenario_ParsesYAML(t *testing.T) {
	tmp := t.TempDir()
	yamlData := []byte(`
scenario: verified_user_creates_channel_room
source: docs/superpowers/specs/example.md
input:
  verb: nats_request
  subject: chat.user.${requester.account}.request.room.${site}.create
  payload:
    name: $auto
    requesterAccount: ${requester.account}
  credential: ${requester.credential}
  placeholders:
    requester:
      type: user
      predicate:
        verified: true
sequence:
  - service: room-service
    reads:
      - location: reply
        matcher: matches_shape
        expected:
          name: $payload.name
      - location: mongo.rooms
        matcher: count_eq
        expected: 1
mishaps:
  ignore: []
`)
	path := filepath.Join(tmp, "test.yaml")
	require.NoError(t, os.WriteFile(path, yamlData, 0o644))

	s, err := LoadFile(path)
	require.NoError(t, err)

	assert.Equal(t, "verified_user_creates_channel_room", s.Name)
	assert.Equal(t, "nats_request", s.Input.Verb)
	assert.Len(t, s.Sequence, 1)
	assert.Equal(t, "room-service", s.Sequence[0].Service)
	assert.Len(t, s.Sequence[0].Reads, 2)
}
