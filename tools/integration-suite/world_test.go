package integrationsuite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorld_PrefixForScenario_GeneratesUniquePrefixerPerScenario(t *testing.T) {
	w := NewWorld("7a2c")

	w.BeginScenario("Adding a member")
	p1 := w.Prefix()
	assert.NotNil(t, p1)
	assert.Equal(t, "it-7a2c-adding-a-member-alice", p1.ID("alice"))

	w.BeginScenario("Removing a member")
	p2 := w.Prefix()
	require.NotNil(t, p2)
	assert.NotEqual(t, p1.ID("alice"), p2.ID("alice"))
}

func TestWorld_StoresLastResponse(t *testing.T) {
	w := NewWorld("7a2c")
	w.BeginScenario("Scenario A")

	w.SetLastResponse(&LastResponse{
		StatusCode: 404,
		Body:       []byte(`{"code":"ROOM_NOT_FOUND"}`),
		TraceID:    "abc123",
	})

	got := w.LastResponse()
	require.NotNil(t, got)
	assert.Equal(t, 404, got.StatusCode)
	assert.Equal(t, "abc123", got.TraceID)
}

func TestWorld_ResponseClearsBetweenScenarios(t *testing.T) {
	w := NewWorld("7a2c")
	w.BeginScenario("Scenario A")
	w.SetLastResponse(&LastResponse{StatusCode: 200})

	w.BeginScenario("Scenario B")
	assert.Nil(t, w.LastResponse(), "last response must reset between scenarios")
}
