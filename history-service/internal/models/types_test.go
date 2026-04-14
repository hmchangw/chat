package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundTrip marshals src to JSON and unmarshals into dst, verifying they match.
func roundTrip[T any](t *testing.T, src T) T {
	t.Helper()
	data, err := json.Marshal(src)
	require.NoError(t, err)
	var dst T
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, src, dst)
	return dst
}

func TestParticipant_JSON(t *testing.T) {
	p := Participant{
		ID:          "u1",
		EngName:     "Alice Smith",
		CompanyName: "Acme Corp",
		AppID:       "app-1",
		AppName:     "MyApp",
		IsBot:       true,
		Account:     "alice",
	}
	roundTrip(t, p)
}

func TestParticipant_JSON_Minimal(t *testing.T) {
	p := Participant{ID: "u1", Account: "alice"}
	got := roundTrip(t, p)
	assert.Empty(t, got.EngName)
	assert.False(t, got.IsBot)
}

func TestFile_JSON(t *testing.T) {
	f := File{ID: "f1", Name: "doc.pdf", Type: "application/pdf"}
	roundTrip(t, f)
}

func TestCard_JSON(t *testing.T) {
	c := Card{Template: "approval", Data: []byte(`{"key":"value"}`)}
	roundTrip(t, c)
}

func TestCard_JSON_NilData(t *testing.T) {
	c := Card{Template: "simple"}
	roundTrip(t, c)
}

func TestCardAction_JSON(t *testing.T) {
	ca := CardAction{
		Verb:        "approve",
		Text:        "Approve",
		CardID:      "c1",
		DisplayText: "Click to approve",
		HideExecLog: true,
		CardTmID:    "tm1",
		Data:        []byte(`{"action":"yes"}`),
	}
	roundTrip(t, ca)
}

func TestCardAction_JSON_Minimal(t *testing.T) {
	ca := CardAction{Verb: "click"}
	got := roundTrip(t, ca)
	assert.Empty(t, got.Text)
	assert.Empty(t, got.CardID)
	assert.False(t, got.HideExecLog)
}

func TestUnmarshalUDT_UnknownField(t *testing.T) {
	assert.NoError(t, (&Participant{}).UnmarshalUDT("nonexistent", nil, nil))
	assert.NoError(t, (&File{}).UnmarshalUDT("nonexistent", nil, nil))
	assert.NoError(t, (&Card{}).UnmarshalUDT("nonexistent", nil, nil))
	assert.NoError(t, (&CardAction{}).UnmarshalUDT("nonexistent", nil, nil))
}

func TestMarshalUDT_UnknownField(t *testing.T) {
	data, err := (&Participant{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)

	data, err = (&File{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)

	data, err = (&Card{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)

	data, err = (&CardAction{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)
}

func TestVerifyUDTTags_PanicsOnMissingTag(t *testing.T) {
	type BadUDT struct {
		Name string `cql:"name"`
		Oops string // no cql tag
	}
	assert.Panics(t, func() { verifyUDTTags(&BadUDT{}) })
}
