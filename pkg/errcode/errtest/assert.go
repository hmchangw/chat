// Package errtest provides assertions for errcode wire envelopes in tests.
package errtest

import (
	"testing"

	"github.com/hmchangw/chat/pkg/errcode"
)

// Decode parses an error envelope from a reply payload, failing the test if it
// is not one. testing.TB so helpers compose with sub-test/bench/mock callers.
func Decode(t testing.TB, data []byte) *errcode.Error {
	t.Helper()
	e, ok := errcode.Parse(data)
	if !ok {
		t.Fatalf("payload is not an error envelope: %s", data)
		return nil // unreachable on real *testing.T; lets recording mocks return cleanly
	}
	return e
}

// AssertCode fails unless data is an error envelope with the given code.
func AssertCode(t testing.TB, data []byte, want errcode.Code) {
	t.Helper()
	e := Decode(t, data)
	if e == nil {
		return
	}
	if got := e.Code; got != want {
		t.Fatalf("code = %q, want %q (payload %s)", got, want, data)
	}
}

// AssertReason fails unless data is an error envelope with the given reason.
func AssertReason(t testing.TB, data []byte, want errcode.Reason) {
	t.Helper()
	e := Decode(t, data)
	if e == nil {
		return
	}
	if got := e.Reason; got != want {
		t.Fatalf("reason = %q, want %q (payload %s)", got, want, data)
	}
}
