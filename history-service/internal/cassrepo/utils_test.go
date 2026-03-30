package cassrepo

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCursor_Empty(t *testing.T) {
	c, err := NewCursor("")
	require.NoError(t, err)
	assert.Empty(t, c.Raw())
	assert.Equal(t, "", c.Encode())
}

func TestNewCursor_Valid(t *testing.T) {
	state := []byte{0x01, 0x02, 0x03}
	encoded := base64.StdEncoding.EncodeToString(state)

	c, err := NewCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, state, c.Raw())
	assert.Equal(t, encoded, c.Encode())
}

func TestNewCursor_Invalid(t *testing.T) {
	_, err := NewCursor("not-valid-base64!!!")
	require.Error(t, err)
}

func TestCursor_RoundTrip(t *testing.T) {
	state := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	original := &Cursor{state: state}

	encoded := original.Encode()
	decoded, err := NewCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, state, decoded.Raw())
}

func TestNewPage(t *testing.T) {
	data := []string{"a", "b", "c"}
	state := []byte{0x01, 0x02}

	page := NewPage(data, state)

	assert.Equal(t, data, page.Data)
	assert.True(t, page.HasNext)
	assert.NotEmpty(t, page.NextCursor)
}

func TestNewPage_LastPage(t *testing.T) {
	data := []string{"a"}

	page := NewPage(data, nil)

	assert.Equal(t, data, page.Data)
	assert.False(t, page.HasNext)
	assert.Empty(t, page.NextCursor)
}

func TestParseQuery_Defaults(t *testing.T) {
	q, err := ParseQuery("", 0)
	require.NoError(t, err)
	assert.Empty(t, q.Cursor.Raw())
	assert.Equal(t, 10, q.PageSize)
}

func TestParseQuery_WithValues(t *testing.T) {
	state := []byte{0x01}
	encoded := base64.StdEncoding.EncodeToString(state)

	q, err := ParseQuery(encoded, 25)
	require.NoError(t, err)
	assert.Equal(t, state, q.Cursor.Raw())
	assert.Equal(t, 25, q.PageSize)
}

func TestParseQuery_InvalidCursor(t *testing.T) {
	_, err := ParseQuery("bad!!!", 10)
	require.Error(t, err)
}

func TestParseQuery_ClampsPageSize(t *testing.T) {
	q, err := ParseQuery("", 999)
	require.NoError(t, err)
	assert.Equal(t, 10, q.PageSize)
}

func TestQueryBuilder_Chaining(t *testing.T) {
	cursor := &Cursor{state: []byte{0xAB}}
	b := NewQueryBuilder(nil).
		WithCursor(cursor).
		WithPageSize(25)

	assert.Equal(t, cursor, b.cursor)
	assert.Equal(t, 25, b.pageSize)
}
