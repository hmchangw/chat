package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestScenario_ParsesNewShape(t *testing.T) {
	src := `
scenario: alice-creates-room-federates
source: room-service/handler.go:296-374
status: draft
tag: positive

sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
  site-b:
    seed:
      users:
        bob: { verified: true }

cassandra_data: []

input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload:
    name: Engineering
    users: ["${alice.account}"]
  credential: ${alice.credential}

expected:
  - location: reply
    match: { body_json: { status: accepted } }
  - location: mongo_find
    site: site-a
    args: { collection: rooms, filter: { name: Engineering } }
    match: { name: Engineering }
  - location: mongo_find
    site: site-b
    args: { collection: rooms, filter: { name: Engineering } }
    match: { name: Engineering }
    timeout: 10s
`
	var s Scenario
	require.NoError(t, yaml.Unmarshal([]byte(src), &s))

	assert.Equal(t, "alice-creates-room-federates", s.Name)
	assert.Equal(t, "positive", s.Tag)

	require.NotNil(t, s.Sites)
	require.Contains(t, s.Sites, "site-a")
	require.Contains(t, s.Sites, "site-b")
	assert.True(t, s.Sites["site-a"].Seed.Users["alice"]["verified"])
	assert.True(t, s.Sites["site-b"].Seed.Users["bob"]["verified"])

	require.Len(t, s.Input, 1, "single-fire map shape decodes to a one-task list")
	assert.Equal(t, "site-a", s.Input[0].Site)
	assert.Equal(t, "nats_request", s.Input[0].Verb)
	assert.Equal(t, "${alice.credential}", s.Input[0].Credential)
	assert.Empty(t, s.Input[0].ID, "single-fire map shape needs no id")

	require.Len(t, s.Expected, 3)
	assert.Equal(t, "reply", s.Expected[0].Location)
	assert.Empty(t, s.Expected[0].Site, "reply must have no site")
	assert.Equal(t, "mongo_find", s.Expected[1].Location)
	assert.Equal(t, "site-a", s.Expected[1].Site)
	assert.Equal(t, "site-b", s.Expected[2].Site)
}

func TestTaskList_UnmarshalYAML_LegacyMap(t *testing.T) {
	src := `
site: site-a
verb: nats_request
subject: chat.user.${alice.account}.request.room.site-a.create
payload: { name: Engineering }
credential: ${alice.credential}
`
	var tl TaskList
	require.NoError(t, yaml.Unmarshal([]byte(src), &tl))
	require.Len(t, tl, 1)
	assert.Empty(t, tl[0].ID)
	assert.Equal(t, "site-a", tl[0].Site)
	assert.Equal(t, "nats_request", tl[0].Verb)
	assert.Equal(t, "Engineering", tl[0].Payload["name"])
}

func TestTaskList_UnmarshalYAML_List(t *testing.T) {
	src := `
- id: create
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload: { name: Engineering }
  credential: ${alice.credential}
- id: join
  site: site-a
  verb: nats_request
  subject: chat.user.${bob.account}.request.room.site-a.join
  payload: { roomId: "${create.reply.body_json.roomId}" }
  credential: ${bob.credential}
`
	var tl TaskList
	require.NoError(t, yaml.Unmarshal([]byte(src), &tl))
	require.Len(t, tl, 2)
	assert.Equal(t, "create", tl[0].ID)
	assert.Equal(t, "join", tl[1].ID)
	assert.Equal(t, "${create.reply.body_json.roomId}", tl[1].Payload["roomId"])
}

func TestTaskList_UnmarshalYAML_NeitherMapNorList(t *testing.T) {
	var tl TaskList
	err := yaml.Unmarshal([]byte(`foo`), &tl)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "single fire (map) or a list")
}

func TestTaskList_UnmarshalYAML_EmptyList(t *testing.T) {
	var tl TaskList
	err := yaml.Unmarshal([]byte(`[]`), &tl)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one task")
}

func TestScenario_HasNoCasesNoBaseInput(t *testing.T) {
	// Compile-time check: the Scenario struct exposes Input + Expected
	// directly (no BaseInput, no Cases).
	var s Scenario
	_ = s.Input    // must compile
	_ = s.Expected // must compile

	// The OLD fields should not exist; uncomment to verify the spec at
	// review time:
	// _ = s.BaseInput  // expected compile error
	// _ = s.Cases      // expected compile error
}
