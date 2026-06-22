package main

import (
	"reflect"

	"github.com/hmchangw/chat/pkg/jsonwarm"
	"github.com/hmchangw/chat/pkg/model"
)

// pretouchTypes are the hot types whose sonic codecs are warmed at startup.
// SendMessageRequest is the untrusted client-input decode; MessageEvent is the
// canonical publish.
//
// cassandra.Message is intentionally absent: it embeds the marshal-only Reactions
// map (struct-keyed, no UnmarshalJSON) whose decoder sonic rejects.
// fetcher_history.go keeps that one unmarshal on encoding/json.
var pretouchTypes = []reflect.Type{
	reflect.TypeOf(model.SendMessageRequest{}),
	reflect.TypeOf(model.MessageEvent{}),
	reflect.TypeOf(model.Message{}),
}

func pretouchJSON() { jsonwarm.Pretouch(pretouchTypes...) }
