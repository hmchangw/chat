package errcode

import "testing"

func TestParse_ErrorEnvelope(t *testing.T) {
	e, ok := Parse([]byte(`{"code":"forbidden","reason":"not_room_member","error":"only room members can perform this action"}`))
	if !ok || e.Code != CodeForbidden || e.Reason != "not_room_member" {
		t.Fatalf("parse failed: %+v ok=%v", e, ok)
	}
}

func TestParse_NonErrorJSON(t *testing.T) {
	if _, ok := Parse([]byte(`{"roomId":"r1","status":"accepted"}`)); ok {
		t.Fatal("payload without non-empty error must not parse as error")
	}
}

func TestParse_Malformed(t *testing.T) {
	if _, ok := Parse([]byte(`not json`)); ok {
		t.Fatal("malformed must not parse")
	}
}
