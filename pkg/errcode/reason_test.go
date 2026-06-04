package errcode

import "testing"

func TestReason_IsString(t *testing.T) {
	var r Reason = "max_room_size_reached"
	if string(r) != "max_room_size_reached" {
		t.Fatal("Reason must be a string-backed type")
	}
}
