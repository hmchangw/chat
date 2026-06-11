package errtest

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hmchangw/chat/pkg/errcode"
)

func TestAssertEnvelope(t *testing.T) {
	data, _ := json.Marshal(errcode.NotFound("room not found", errcode.WithReason(errcode.RoomNotMember)))
	AssertCode(t, data, errcode.CodeNotFound)
	AssertReason(t, data, errcode.RoomNotMember)
}

// recordingT implements the subset of testing.TB that the errtest helpers use
// (Helper, Fatalf). Embedding testing.T fills out the rest of the interface
// at compile time; we override Fatalf to capture instead of abort so the
// outer test can assert on the recorded message.
type recordingT struct {
	testing.T
	failed bool
	msg    string
}

func (r *recordingT) Helper() {}

func (r *recordingT) Fatalf(format string, args ...any) {
	r.failed = true
	r.msg = fmt.Sprintf(format, args...)
	// Do NOT terminate; just record. The helper returns early via the
	// nil-check guard in assert.go.
}

func TestDecode_FailsOnNonEnvelope(t *testing.T) {
	rt := &recordingT{}
	got := Decode(rt, []byte(`{"not":"an_envelope"}`))
	if !rt.failed {
		t.Fatal("Decode must call Fatalf on a non-envelope payload")
	}
	if got != nil {
		t.Fatalf("Decode must return nil on Fatalf path, got %+v", got)
	}
	if rt.msg == "" {
		t.Fatal("Fatalf message should describe the failure")
	}
}

func TestAssertCode_FailsOnMismatch(t *testing.T) {
	data, _ := json.Marshal(errcode.NotFound("x"))
	rt := &recordingT{}
	AssertCode(rt, data, errcode.CodeConflict)
	if !rt.failed {
		t.Fatal("AssertCode must call Fatalf when the envelope code does not match")
	}
}

func TestAssertReason_FailsOnMismatch(t *testing.T) {
	data, _ := json.Marshal(errcode.Forbidden("x", errcode.WithReason(errcode.RoomNotMember)))
	rt := &recordingT{}
	AssertReason(rt, data, errcode.RoomNotOwner)
	if !rt.failed {
		t.Fatal("AssertReason must call Fatalf when the envelope reason does not match")
	}
}

func TestAssertCode_PassesSilentlyOnMatch(t *testing.T) {
	data, _ := json.Marshal(errcode.NotFound("x"))
	rt := &recordingT{}
	AssertCode(rt, data, errcode.CodeNotFound)
	if rt.failed {
		t.Fatalf("AssertCode must not fail on matching code; recorded: %s", rt.msg)
	}
}

func TestAssertReason_PassesSilentlyOnMatch(t *testing.T) {
	data, _ := json.Marshal(errcode.Forbidden("x", errcode.WithReason(errcode.RoomNotOwner)))
	rt := &recordingT{}
	AssertReason(rt, data, errcode.RoomNotOwner)
	if rt.failed {
		t.Fatalf("AssertReason must not fail on matching reason; recorded: %s", rt.msg)
	}
}
