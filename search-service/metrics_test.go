package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hmchangw/chat/pkg/errcode"
)

func TestStatusLabel_OkOnNil(t *testing.T) {
	if got := statusLabel(nil); got != "ok" {
		t.Fatalf("nil err → status = %q, want %q", got, "ok")
	}
}

func TestStatusLabel_CanonicalErrcodePassesThrough(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errcode.BadRequest("x"), "bad_request"},
		{errcode.Unauthenticated("x"), "unauthenticated"},
		{errcode.Forbidden("x"), "forbidden"},
		{errcode.NotFound("x"), "not_found"},
		{errcode.Conflict("x"), "conflict"},
		{errcode.TooManyRequests("x"), "too_many_requests"},
		{errcode.Unavailable("x"), "unavailable"},
		{errcode.Internal("x"), "internal"},
	}
	for _, tc := range cases {
		if got := statusLabel(tc.err); got != tc.want {
			t.Errorf("statusLabel(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// Wrapped *errcode.Error (the actual production shape from handler.go where
// callers fmt.Errorf("ctx: %w", errcodeErr) before returning) must traverse
// the chain via errors.As and still pin the right label.
func TestStatusLabel_WrappedErrcodePassesThrough(t *testing.T) {
	wrapped := fmt.Errorf("handler load: %w", errcode.BadRequest("missing field"))
	if got := statusLabel(wrapped); got != "bad_request" {
		t.Fatalf("wrapped errcode → %q, want bad_request", got)
	}
}

func TestStatusLabel_NonCanonicalCodeCollapsesToInternal(t *testing.T) {
	// Synthetic *errcode.Error with a non-canonical Code (e.g. a federation peer
	// shipped a foreign envelope). Must not mint a new Prometheus series — the
	// allowedStatusLabels guard collapses it to "internal".
	bad := &errcode.Error{Code: errcode.Code("made_up_category"), Message: "x"}
	if got := statusLabel(bad); got != "internal" {
		t.Fatalf("non-canonical Code → status = %q, want %q", got, "internal")
	}
}

func TestStatusLabel_RawErrorCollapsesToInternal(t *testing.T) {
	if got := statusLabel(errors.New("mongo down")); got != "internal" {
		t.Fatalf("raw err → status = %q, want %q", got, "internal")
	}
	wrapped := fmt.Errorf("ctx: %w", errors.New("mongo down"))
	if got := statusLabel(wrapped); got != "internal" {
		t.Fatalf("wrapped raw err → status = %q, want %q", got, "internal")
	}
}
