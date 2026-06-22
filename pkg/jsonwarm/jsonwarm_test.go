package jsonwarm

import (
	"reflect"
	"testing"
)

// TestPretouch_CompilableTypes warms ordinary types — must complete without panic.
func TestPretouch_CompilableTypes(t *testing.T) {
	type inner struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	Pretouch(reflect.TypeOf(inner{}), reflect.TypeOf([]inner{}))
}

// TestPretouch_UndecodableType_NonFatal warms a struct-keyed map (no valid JSON
// decoder); Pretouch must log-and-continue, never panic or fail the caller.
func TestPretouch_UndecodableType_NonFatal(t *testing.T) {
	type key struct{ X string }
	Pretouch(reflect.TypeOf(map[key]int{}))
}

// TestPretouch_Empty is a no-op call — must be safe.
func TestPretouch_Empty(t *testing.T) {
	Pretouch()
}
