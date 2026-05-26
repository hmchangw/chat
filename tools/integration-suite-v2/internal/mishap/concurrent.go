package mishap

// ConcurrentExecutor is the concurrency mishap. For v2-Part1 it is
// instantiated by the runner with N=3 distinct fixtures; the actual
// fan-out logic lives in the runner because it needs access to the
// fixture cast and verb dispatcher.
//
// This struct exists so the catalog can reference an executor name;
// the Apply/Cleanup are no-ops because the dispatcher does the fan-out
// itself.
type ConcurrentExecutor struct{ N int }

func (c *ConcurrentExecutor) Apply() error   { return nil }
func (c *ConcurrentExecutor) Cleanup() error { return nil }
