package errcode

import (
	"errors"
	"fmt"
	"testing"
)

func TestPermanent_PanicsOnNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Permanent(nil) must panic")
		}
	}()
	Permanent(nil)
}

func TestPermanent_UnwrapReachesErrcode(t *testing.T) {
	inner := NotFound("room not found", WithReason("room_not_found"))
	p := Permanent(inner)
	var got *Error
	if !errors.As(p, &got) {
		t.Fatal("errors.As must reach the wrapped *Error")
	}
	if got.Code != CodeNotFound || got.Reason != "room_not_found" {
		t.Fatalf("wrapped *Error lost: %+v", got)
	}
}

func TestPermanent_IsMatchesSentinel(t *testing.T) {
	p := Permanent(Internal("boom"))
	if !errors.Is(p, ErrPermanent) {
		t.Fatal("errors.Is(p, ErrPermanent) must hold")
	}
	wrapped := fmt.Errorf("publish: %w", p)
	if !errors.Is(wrapped, ErrPermanent) {
		t.Fatal("errors.Is must traverse the wrap")
	}
}

func TestIsPermanent_DetectsWrapper(t *testing.T) {
	inner := Forbidden("denied")
	p := Permanent(inner)
	ec, ok := IsPermanent(p)
	if !ok {
		t.Fatal("IsPermanent must return true on wrapped")
	}
	if ec.Code != CodeForbidden {
		t.Fatalf("wrapped *Error lost: %+v", ec)
	}
}

func TestIsPermanent_FalseOnPlainErrcode(t *testing.T) {
	if _, ok := IsPermanent(Internal("boom")); ok {
		t.Fatal("plain *Error is not permanent")
	}
	if _, ok := IsPermanent(errors.New("raw")); ok {
		t.Fatal("raw error is not permanent")
	}
	if _, ok := IsPermanent(nil); ok {
		t.Fatal("nil is not permanent")
	}
}
