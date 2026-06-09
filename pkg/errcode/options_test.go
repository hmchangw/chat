package errcode

import (
	"errors"
	"fmt"
	"testing"
)

func TestNamedConstructors(t *testing.T) {
	if e := BadRequest("name is required"); e.Code != CodeBadRequest || e.Message != "name is required" {
		t.Fatalf("BadRequest: %+v", e)
	}
	if e := NotFound("gone"); e.Code != CodeNotFound {
		t.Fatal("NotFound")
	}
	for _, e := range []*Error{
		Unauthenticated("x"), Forbidden("x"), Conflict("x"),
		TooManyRequests("x"), Unavailable("x"), Internal("x"),
	} {
		if e.Message != "x" {
			t.Fatalf("constructor message: %+v", e)
		}
	}
	// Spot-check that TooManyRequests sets the 429 category specifically
	// (cheap pin against future copy-paste regressions).
	if e := TooManyRequests("rate limited"); e.Code != CodeTooManyRequests {
		t.Fatalf("TooManyRequests Code = %q, want %q", e.Code, CodeTooManyRequests)
	}
}

func TestConstructorDoesNotFormat_LiteralPercentIsSafe(t *testing.T) {
	if got := BadRequest("100% full").Message; got != "100% full" {
		t.Fatalf("constructor must not format: %q", got)
	}
}

func TestFormattingPlusOptionUsesSprintfAtCallSite(t *testing.T) {
	// The supported pattern for dynamic text + a reason: caller formats, options stay first-class.
	e := Conflict(fmt.Sprintf("room %s is full", "r1"), WithReason("max_room_size_reached"))
	if e.Message != "room r1 is full" || e.Reason != "max_room_size_reached" {
		t.Fatalf("got %+v", e)
	}
}

func TestWithReason(t *testing.T) {
	e := BadRequest("room full", WithReason("max_room_size_reached"))
	if e.Reason != "max_room_size_reached" {
		t.Fatalf("reason = %q", e.Reason)
	}
}

func TestWithMetadata_Pairs(t *testing.T) {
	e := Conflict("dm exists", WithMetadata("roomId", "r1", "kind", "dm"))
	if e.Metadata["roomId"] != "r1" || e.Metadata["kind"] != "dm" {
		t.Fatalf("meta = %v", e.Metadata)
	}
}

func TestWithMetadata_OddArgsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("odd WithMetadata args must panic")
		}
	}()
	BadRequest("x", WithMetadata("lonely"))
}

func TestWithCause_RawError(t *testing.T) {
	root := errors.New("mongo down")
	if e := Internal("internal error", WithCause(root)); !errors.Is(e, root) {
		t.Fatal("cause not attached")
	}
}

func TestWithCause_PanicsOnNestedErrcode(t *testing.T) {
	inner := NotFound("room not found")
	defer func() {
		if recover() == nil {
			t.Fatal("WithCause(errcode.Error) must panic — invariant: one *Error per chain")
		}
	}()
	Internal("x", WithCause(inner))
}

func TestWithCause_PanicsOnWrappedNestedErrcode(t *testing.T) {
	inner := NotFound("room not found")
	wrapped := fmt.Errorf("ctx: %w", inner)
	defer func() {
		if recover() == nil {
			t.Fatal("WithCause must detect *Error even when wrapped")
		}
	}()
	Internal("x", WithCause(wrapped))
}

func TestNew_PanicsOnUnknownCategory(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New must panic on a non-canonical Code")
		}
	}()
	New(Code("made_up"), "msg")
}

func TestNew_PanicsOnEmptyMessage(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New must panic on an empty message")
		}
	}()
	New(CodeNotFound, "")
}

func TestNamedConstructor_PanicsOnEmptyMessage(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("named constructors must inherit the empty-message panic")
		}
	}()
	NotFound("")
}
