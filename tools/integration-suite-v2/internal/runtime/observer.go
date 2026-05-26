package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
)

// Observer fans out reader Watch channels into one merged Event stream
// for the timeframe closer + verdict to consume. Also applies
// SCOPE-AWARE synthetic trace stamping: for any event arriving with
// empty Traceparent whose OwnerSvc is in this scenario's sequence, the
// observer stamps the scenario's traceparent. Out-of-scope downstream
// cascades (e.g. a Mongo write by a service not in s.Sequence) stay
// trace-less and fall to profile-based background filtering at verdict
// time.
type Observer struct {
	Readers *readers.Registry
}

// Start begins watching every reader. `traceparent` is the scenario's
// trace; `inScopeServices` are the services declared in s.Sequence.
// The merged channel closes when ctx is cancelled (typically by the
// timeframe closer at T_close).
func (o *Observer) Start(ctx context.Context, traceparent string, inScopeServices []string, startTime time.Time) <-chan readers.Event {
	merged := make(chan readers.Event, 256)
	var wg sync.WaitGroup

	inScope := map[string]bool{}
	for _, s := range inScopeServices {
		inScope[s] = true
	}

	for _, r := range o.Readers.All() {
		ch, err := r.Watch(ctx, traceparent, startTime)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(c <-chan readers.Event) {
			defer wg.Done()
			for ev := range c {
				if ev.Traceparent == "" && inScope[ev.OwnerSvc] {
					ev.Traceparent = traceparent
				}
				select {
				case merged <- ev:
				case <-ctx.Done():
					return
				}
			}
		}(ch)
	}

	go func() {
		wg.Wait()
		close(merged)
	}()

	return merged
}
