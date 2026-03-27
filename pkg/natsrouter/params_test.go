package natsrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePattern_SingleParam(t *testing.T) {
	r := parsePattern("chat.user.{userID}.request")

	assert.Equal(t, "chat.user.*.request", r.natsSubject)
	assert.Equal(t, map[int]string{2: "userID"}, r.params)
}

func TestParsePattern_MultipleParams(t *testing.T) {
	r := parsePattern("chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history")

	assert.Equal(t, "chat.user.*.request.room.*.*.msg.history", r.natsSubject)
	assert.Equal(t, map[int]string{2: "userID", 5: "roomID", 6: "siteID"}, r.params)
}

func TestParsePattern_NoParams(t *testing.T) {
	r := parsePattern("chat.user.request.rooms.list")

	assert.Equal(t, "chat.user.request.rooms.list", r.natsSubject)
	assert.Empty(t, r.params)
}

func TestParsePattern_AdjacentParams(t *testing.T) {
	r := parsePattern("fanout.{siteID}.{roomID}")

	assert.Equal(t, "fanout.*.*", r.natsSubject)
	assert.Equal(t, map[int]string{1: "siteID", 2: "roomID"}, r.params)
}

func TestParsePattern_AllParams(t *testing.T) {
	r := parsePattern("{a}.{b}.{c}")

	assert.Equal(t, "*.*.*", r.natsSubject)
	assert.Equal(t, map[int]string{0: "a", 1: "b", 2: "c"}, r.params)
}

func TestExtractParams(t *testing.T) {
	r := parsePattern("chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history")
	params := r.extractParams("chat.user.alice.request.room.room-42.site-1.msg.history")

	assert.Equal(t, "alice", params.Get("userID"))
	assert.Equal(t, "room-42", params.Get("roomID"))
	assert.Equal(t, "site-1", params.Get("siteID"))
}

func TestExtractParams_NoParams(t *testing.T) {
	r := parsePattern("chat.static.subject")
	params := r.extractParams("chat.static.subject")

	assert.Equal(t, "", params.Get("anything"))
}

func TestParams_Get_NotFound(t *testing.T) {
	p := Params{values: map[string]string{"userID": "abc"}}

	assert.Equal(t, "abc", p.Get("userID"))
	assert.Equal(t, "", p.Get("nonexistent"))
}

func TestParams_MustGet_Success(t *testing.T) {
	p := Params{values: map[string]string{"userID": "abc"}}

	assert.Equal(t, "abc", p.MustGet("userID"))
}

func TestParams_MustGet_Panics(t *testing.T) {
	p := Params{values: map[string]string{}}

	require.Panics(t, func() {
		p.MustGet("nonexistent")
	})
}
