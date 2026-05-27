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

func TestStructScan_NonPointer(t *testing.T) {
	type S struct {
		Name string `cql:"name"`
	}
	// Passing a non-pointer should return false before touching iter.
	ok, err := structScan(nil, S{Name: "x"})
	assert.False(t, ok)
	assert.NoError(t, err)
}

func TestStructScan_PointerToNonStruct(t *testing.T) {
	// Passing a pointer to a non-struct should return false before touching iter.
	s := "hello"
	ok, err := structScan(nil, &s)
	assert.False(t, ok)
	assert.NoError(t, err)
}

// TestBuildScanValues exercises the column-matching helper in isolation,
// covering the unmapped-column path that structScan now surfaces as a hard error.
func TestBuildScanValues(t *testing.T) {
	type Row struct {
		RoomID    string `cql:"room_id"`
		CreatedAt int64  `cql:"created_at"`
		Msg       string `cql:"msg"`
	}

	tests := []struct {
		name        string
		colNames    []string
		wantOK      bool
		wantMissing string
	}{
		{
			name:     "all columns map",
			colNames: []string{"room_id", "created_at", "msg"},
			wantOK:   true,
		},
		{
			name:        "unmapped column at front",
			colNames:    []string{"unknown_col", "room_id"},
			wantOK:      false,
			wantMissing: "unknown_col",
		},
		{
			name:        "unmapped column in middle",
			colNames:    []string{"room_id", "ghost", "msg"},
			wantOK:      false,
			wantMissing: "ghost",
		},
		{
			name:        "unmapped column at end",
			colNames:    []string{"room_id", "created_at", "msg", "extra"},
			wantOK:      false,
			wantMissing: "extra",
		},
		{
			name:     "empty columns",
			colNames: []string{},
			wantOK:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var row Row
			vals, missingCol, ok := buildScanValues(&row, tc.colNames)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantMissing, missingCol)
			if tc.wantOK {
				assert.Len(t, vals, len(tc.colNames))
			}
		})
	}
}

func TestBuildScanValues_NonPointer(t *testing.T) {
	type Row struct {
		ID string `cql:"id"`
	}
	vals, missingCol, ok := buildScanValues(Row{}, []string{"id"})
	assert.False(t, ok)
	assert.Empty(t, missingCol)
	assert.Nil(t, vals)
}

func TestNewCursor_TooLong(t *testing.T) {
	// 515 raw bytes encode to 688 base64 chars, which exceeds EncodedLen(512)=684.
	huge := base64.StdEncoding.EncodeToString(make([]byte, maxCursorBytes+3))
	_, err := NewCursor(huge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode cursor")
}
