package natsutil_test

import (
	"encoding/json"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestMarshalResponse(t *testing.T) {
	room := model.Room{ID: "1", Name: "general"}
	data, err := natsutil.MarshalResponse(room)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got model.Room
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "1" || got.Name != "general" {
		t.Errorf("got %+v", got)
	}
}

func TestMarshalError(t *testing.T) {
	data := natsutil.MarshalError("something went wrong")
	var got model.ErrorResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error != "something went wrong" {
		t.Errorf("got %q", got.Error)
	}
}
