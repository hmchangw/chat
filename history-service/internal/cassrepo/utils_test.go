package cassrepo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewQuery(t *testing.T) {
	q := NewQuery(nil, "SELECT * FROM messages WHERE room_id = ?", "r1")

	assert.Equal(t, "SELECT * FROM messages WHERE room_id = ?", q.stmt)
	assert.Equal(t, []interface{}{"r1"}, q.args)
	assert.Equal(t, 0, q.pageSize)
	assert.Nil(t, q.pageState)
}

func TestQuery_PageSize(t *testing.T) {
	q := NewQuery(nil, "SELECT * FROM t").PageSize(25)

	assert.Equal(t, 25, q.pageSize)
}

func TestQuery_WithPageState(t *testing.T) {
	state := []byte{0x01, 0x02, 0x03}
	q := NewQuery(nil, "SELECT * FROM t").WithPageState(state)

	assert.Equal(t, state, q.pageState)
}

func TestQuery_Chaining(t *testing.T) {
	state := []byte{0xAB}
	q := NewQuery(nil, "SELECT * FROM t", "arg1").
		PageSize(10).
		WithPageState(state)

	assert.Equal(t, "SELECT * FROM t", q.stmt)
	assert.Equal(t, []interface{}{"arg1"}, q.args)
	assert.Equal(t, 10, q.pageSize)
	assert.Equal(t, state, q.pageState)
}
