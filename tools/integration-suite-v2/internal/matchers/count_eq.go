package matchers

import (
	"fmt"
	"reflect"
)

// CountEq reports whether len(observed) == expected. Observed must be a
// slice, array, map, or string. Expected must be int (or convertible).
type CountEq struct{}

func (CountEq) Match(observed, expected any) Result {
	wantInt, ok := toInt(expected)
	if !ok {
		return Result{Reason: fmt.Sprintf("count_eq: expected %#v is not an int", expected)}
	}
	v := reflect.ValueOf(observed)
	switch v.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.String:
		gotLen := v.Len()
		if gotLen == wantInt {
			return Result{Matched: true}
		}
		return Result{Reason: fmt.Sprintf("count_eq: got %d want %d", gotLen, wantInt)}
	default:
		return Result{Reason: fmt.Sprintf("count_eq: observed %#v has no length (kind=%s)", observed, v.Kind())}
	}
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}
