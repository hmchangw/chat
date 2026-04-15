package cassandra

import (
	"testing"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubTypeInfo is a minimal gocql.TypeInfo used for marshal/unmarshal tests.
type stubTypeInfo struct{}

func (s stubTypeInfo) Type() gocql.Type                   { return gocql.TypeVarchar }
func (s stubTypeInfo) Version() byte                      { return 4 }
func (s stubTypeInfo) Custom() string                     { return "" }
func (s stubTypeInfo) New() interface{}                   { return new(string) }
func (s stubTypeInfo) NewWithError() (interface{}, error) { return new(string), nil }

// goodUDT has cql tags on every field — passes verifyUDTTags.
type goodUDT struct {
	Name  string `cql:"name"`
	Value string `cql:"value"`
}

func TestVerifyUDTTags_PanicsOnMissingTag(t *testing.T) {
	type BadUDT struct {
		Name string `cql:"name"`
		Oops string // no cql tag
	}
	assert.Panics(t, func() { verifyUDTTags(&BadUDT{}) })
}

func TestVerifyUDTTags_DoesNotPanicWhenAllTagsPresent(t *testing.T) {
	assert.NotPanics(t, func() { verifyUDTTags(&goodUDT{}) })
}

func TestVerifyUDTTags_PanicsOnNonPointer(t *testing.T) {
	assert.Panics(t, func() { verifyUDTTags(goodUDT{}) })
}

func TestVerifyUDTTags_PanicsOnPointerToNonStruct(t *testing.T) {
	s := "hello"
	assert.Panics(t, func() { verifyUDTTags(&s) })
}

func TestUnmarshalUDTField_KnownField(t *testing.T) {
	u := &goodUDT{}
	info := stubTypeInfo{}
	// Marshal a string value and then unmarshal it back via our helper.
	data, err := gocql.Marshal(info, "hello")
	require.NoError(t, err)
	require.NoError(t, unmarshalUDTField(u, "name", info, data))
	assert.Equal(t, "hello", u.Name)
}

func TestUnmarshalUDTField_UnknownFieldIgnored(t *testing.T) {
	u := &goodUDT{}
	info := stubTypeInfo{}
	data, err := gocql.Marshal(info, "ignored")
	require.NoError(t, err)
	require.NoError(t, unmarshalUDTField(u, "unknown", info, data))
	assert.Equal(t, "", u.Name)
}

func TestUnmarshalUDTField_NonPointerReturnsError(t *testing.T) {
	err := unmarshalUDTField(goodUDT{}, "name", stubTypeInfo{}, nil)
	assert.ErrorContains(t, err, "expected non-nil pointer")
}

func TestUnmarshalUDTField_NilPointerReturnsError(t *testing.T) {
	var u *goodUDT
	err := unmarshalUDTField(u, "name", stubTypeInfo{}, nil)
	assert.ErrorContains(t, err, "expected non-nil pointer")
}

func TestUnmarshalUDTField_PointerToNonStructReturnsError(t *testing.T) {
	s := "hello"
	err := unmarshalUDTField(&s, "name", stubTypeInfo{}, nil)
	assert.ErrorContains(t, err, "expected pointer to struct")
}

func TestMarshalUDTField_KnownField(t *testing.T) {
	u := &goodUDT{Name: "alice", Value: "42"}
	info := stubTypeInfo{}
	data, err := marshalUDTField(u, "name", info)
	require.NoError(t, err)
	require.NotNil(t, data)
	var result string
	require.NoError(t, gocql.Unmarshal(info, data, &result))
	assert.Equal(t, "alice", result)
}

func TestMarshalUDTField_UnknownFieldReturnsNil(t *testing.T) {
	u := &goodUDT{Name: "alice"}
	data, err := marshalUDTField(u, "unknown", stubTypeInfo{})
	require.NoError(t, err)
	assert.Nil(t, data)
}

func TestMarshalUDTField_NonPointerReturnsError(t *testing.T) {
	_, err := marshalUDTField(goodUDT{}, "name", stubTypeInfo{})
	assert.ErrorContains(t, err, "expected non-nil pointer")
}

func TestMarshalUDTField_NilPointerReturnsError(t *testing.T) {
	var u *goodUDT
	_, err := marshalUDTField(u, "name", stubTypeInfo{})
	assert.ErrorContains(t, err, "expected non-nil pointer")
}

func TestMarshalUDTField_PointerToNonStructReturnsError(t *testing.T) {
	s := "hello"
	_, err := marshalUDTField(&s, "name", stubTypeInfo{})
	assert.ErrorContains(t, err, "expected pointer to struct")
}
