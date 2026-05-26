package harness

import (
	"errors"
	"fmt"
	"strings"
)

// BlindspotFailure returns a non-nil error for scenarios tagged with
// any @blindspot:<slug>. Returns nil when slugs is empty.
//
// Returned by the After hook so godog records the scenario as a
// failure with a clear reason; the actual step outcomes are also
// visible in the report.
func BlindspotFailure(slugs []string) error {
	if len(slugs) == 0 {
		return nil
	}
	return fmt.Errorf("undocumented behavior: %s", strings.Join(slugs, ", "))
}

// ErrBlindspot is the sentinel returned (wrapped) by BlindspotFailure.
// Useful if callers want to differentiate via errors.Is later.
var ErrBlindspot = errors.New("blindspot")
