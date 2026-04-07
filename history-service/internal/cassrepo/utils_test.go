package cassrepo

import (
	"encoding/base64"
	"testing"

	"github.com/gocql/gocql"
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

func TestParsePageRequest_Defaults(t *testing.T) {
	q, err := ParsePageRequest("", 0)
	require.NoError(t, err)
	assert.Empty(t, q.Cursor.Raw())
	assert.Equal(t, 50, q.PageSize)
}

func TestParsePageRequest_WithValues(t *testing.T) {
	state := []byte{0x01}
	encoded := base64.StdEncoding.EncodeToString(state)

	q, err := ParsePageRequest(encoded, 25)
	require.NoError(t, err)
	assert.Equal(t, state, q.Cursor.Raw())
	assert.Equal(t, 25, q.PageSize)
}

func TestParsePageRequest_InvalidCursor(t *testing.T) {
	_, err := ParsePageRequest("bad!!!", 10)
	require.Error(t, err)
}

func TestParsePageRequest_ClampsPageSize(t *testing.T) {
	q, err := ParsePageRequest("", 999)
	require.NoError(t, err)
	assert.Equal(t, 100, q.PageSize)
}

func TestQueryBuilder_Chaining(t *testing.T) {
	cursor := &Cursor{state: []byte{0xAB}}
	b := NewQueryBuilder(nil).
		WithCursor(cursor).
		WithPageSize(25)

	assert.Equal(t, cursor, b.cursor)
	assert.Equal(t, 25, b.pageSize)
}

func TestQueryBuilder_Fetch_NilQuery(t *testing.T) {
	b := NewQueryBuilder(nil)
	_, err := b.Fetch(func(iter *gocql.Iter) {
		t.Fatal("scan should not be called for nil query")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil query")
}

func TestNewCursor_Invalid_WrapsError(t *testing.T) {
	_, err := NewCursor("not-valid-base64!!!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode cursor")
}

func TestParsePageRequest_InvalidCursor_WrapsError(t *testing.T) {
	_, err := ParsePageRequest("bad!!!", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse page request cursor")
}
