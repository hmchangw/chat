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
		UserName:    "alice",
		EngName:     "Alice Smith",
		CompanyName: "Acme Corp",
		AppID:       "app-1",
		AppName:     "MyApp",
		IsBot:       true,
	}
	roundTrip(t, p)
}

func TestParticipant_JSON_Minimal(t *testing.T) {
	p := Participant{ID: "u1", UserName: "alice"}
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

func TestParticipant_UnmarshalUDT_UnknownField(t *testing.T) {
	// Unknown fields should be silently ignored (return nil).
	p := &Participant{}
	err := p.UnmarshalUDT("nonexistent_field", nil, nil)
	assert.NoError(t, err)
}

func TestFile_UnmarshalUDT_UnknownField(t *testing.T) {
	f := &File{}
	err := f.UnmarshalUDT("nonexistent_field", nil, nil)
	assert.NoError(t, err)
}

func TestCard_UnmarshalUDT_UnknownField(t *testing.T) {
	c := &Card{}
	err := c.UnmarshalUDT("nonexistent_field", nil, nil)
	assert.NoError(t, err)
}

func TestCardAction_UnmarshalUDT_UnknownField(t *testing.T) {
	ca := &CardAction{}
	err := ca.UnmarshalUDT("nonexistent_field", nil, nil)
	assert.NoError(t, err)
}

func TestParticipant_MarshalUDT_UnknownField(t *testing.T) {
	p := &Participant{ID: "u1"}
	data, err := p.MarshalUDT("nonexistent_field", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)
}
