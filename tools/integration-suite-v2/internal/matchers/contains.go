package matchers

import (
	"fmt"
	"reflect"
	"strings"
)

// Contains: substring (for strings) or item-in-slice (for slices).
type Contains struct{}

func (Contains) Match(observed, expected any) Result {
	if o, ok := observed.(string); ok {
		want, wok := expected.(string)
		if !wok {
			return Result{Reason: fmt.Sprintf("contains: observed is string, expected must be string, got %T", expected)}
		}
		if strings.Contains(o, want) {
			return Result{Matched: true}
		}
		return Result{Reason: fmt.Sprintf("contains: substring %q not in %q", want, o)}
	}

	v := reflect.ValueOf(observed)
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		for i := 0; i < v.Len(); i++ {
			if reflect.DeepEqual(v.Index(i).Interface(), expected) {
				return Result{Matched: true}
			}
		}
		return Result{Reason: fmt.Sprintf("contains: item %#v not in observed slice", expected)}
	}

	return Result{Reason: fmt.Sprintf("contains: observed %T not searchable", observed)}
}
