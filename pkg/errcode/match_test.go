package errcode

import (
	"errors"
	"fmt"
	"testing"
)

func TestReasonOf(t *testing.T) {
	err := fmt.Errorf("ctx: %w", NotFound("x", WithReason(RoomNotMember)))
	if ReasonOf(err) != RoomNotMember {
		t.Fatalf("ReasonOf = %q", ReasonOf(err))
	}
	if ReasonOf(errors.New("plain")) != "" {
		t.Fatal("non-errcode error must yield empty reason")
	}
}

func TestHasReason(t *testing.T) {
	if !HasReason(NotFound("x", WithReason(RoomNotMember)), RoomNotMember) {
		t.Fatal("HasReason should match")
	}
	if HasReason(NotFound("x"), RoomNotMember) {
		t.Fatal("HasReason must not match an absent reason")
	}
}
