package errcode

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestError_Error_ReturnsMessageOnly(t *testing.T) {
	e := &Error{Code: CodeBadRequest, Message: "name is required", cause: errors.New("secret db detail")}
	if e.Error() != "name is required" {
		t.Fatalf("Error() = %q, want safe message only", e.Error())
	}
}

func TestError_Unwrap(t *testing.T) {
	root := errors.New("root")
	e := &Error{Code: CodeInternal, Message: "internal error", cause: root}
	if !errors.Is(e, root) {
		t.Fatal("errors.Is should reach the wrapped cause via Unwrap")
	}
}

func TestError_MarshalJSON_NeverLeaksCause(t *testing.T) {
	e := &Error{
		Code:     CodeBadRequest,
		Reason:   "max_room_size_reached",
		Message:  "room is full",
		Metadata: map[string]string{"limit": "500"},
		cause:    errors.New("mongo: connection refused at 10.0.0.5"),
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"bad_request","reason":"max_room_size_reached","error":"room is full","metadata":{"limit":"500"}}`
	if string(b) != want {
		t.Fatalf("marshal = %s, want %s", b, want)
	}
	if strings.Contains(string(b), "mongo") {
		t.Fatal("cause leaked into JSON")
	}
}

func TestError_MarshalJSON_OmitsEmptyOptionalFields(t *testing.T) {
	b, _ := json.Marshal(&Error{Code: CodeNotFound, Message: "not found"})
	if want := `{"code":"not_found","error":"not found"}`; string(b) != want {
		t.Fatalf("marshal = %s, want %s", b, want)
	}
}

func TestError_HTTPStatus(t *testing.T) {
	if (&Error{Code: CodeNotFound}).HTTPStatus() != 404 {
		t.Fatal("HTTPStatus should delegate to Code.HTTPStatus")
	}
}
