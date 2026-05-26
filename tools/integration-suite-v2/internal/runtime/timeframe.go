package runtime

import (
	"strings"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
)

// GatherUntilQuiet collects events from the channel until either:
//
//	(a) no in-scope event has arrived for `quietGrace` duration, OR
//	(b) `safetyCap` total time has elapsed since first call.
//
// An "in-scope event" is one whose Traceparent matches our traceID AND
// whose OwnerSvc is in inScopeServices.
//
// Returns (every event collected, the close time T_close).
func GatherUntilQuiet(
	in <-chan readers.Event,
	traceID string,
	inScopeServices []string,
	quietGrace time.Duration,
	safetyCap time.Duration,
) ([]readers.Event, time.Time) {
	inScope := map[string]bool{}
	for _, s := range inScopeServices {
		inScope[s] = true
	}

	var gathered []readers.Event
	lastActivityAt := time.Now()
	sawAny := false
	deadline := time.NewTimer(safetyCap)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case ev, ok := <-in:
			if !ok {
				return gathered, time.Now()
			}
			gathered = append(gathered, ev)
			sawAny = true
			if hasTrace(ev.Traceparent, traceID) && inScope[ev.OwnerSvc] {
				lastActivityAt = ev.Timestamp
			} else {
				lastActivityAt = time.Now()
			}
		case <-tick.C:
			if sawAny && time.Since(lastActivityAt) >= quietGrace {
				return gathered, time.Now()
			}
		case <-deadline.C:
			return gathered, time.Now()
		}
	}
}

func hasTrace(tp, traceID string) bool {
	return strings.Contains(tp, traceID)
}
