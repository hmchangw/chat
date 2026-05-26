package matchers

import (
	"fmt"
	"reflect"
)

// Equals reports deep equality via reflect.DeepEqual.
type Equals struct{}

func (Equals) Match(observed, expected any) Result {
	if reflect.DeepEqual(observed, expected) {
		return Result{Matched: true}
	}
	return Result{Reason: fmt.Sprintf("equals: observed=%#v expected=%#v", observed, expected)}
}
