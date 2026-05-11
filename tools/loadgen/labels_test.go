package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// kindLabel default-branch coverage — passing an out-of-range enum value
// hits the "unknown" arm, locking down the contract that all three label
// helpers degrade gracefully rather than panicking.
func TestHistoryKindLabel_Unknown(t *testing.T) {
	assert.Equal(t, "unknown", historyKindLabel(historyRequestKind(99)))
}
func TestSearchKindLabel_Unknown(t *testing.T) {
	assert.Equal(t, "unknown", searchKindLabel(searchRequestKind(99)))
}
func TestRoomKindLabel_Unknown(t *testing.T) {
	assert.Equal(t, "unknown", roomKindLabel(roomRequestKind(99)))
}
