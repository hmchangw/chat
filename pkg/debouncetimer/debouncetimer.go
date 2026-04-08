package debouncetimer

import (
	"sync"
	"time"
)

// Timer is a resettable debounce timer. Call Reset to start or restart the
// countdown. The callback fires once after the duration elapses without
// another Reset. Call Stop to cancel permanently; after Stop, Reset is a no-op.
type Timer struct {
	mu       sync.Mutex
	duration time.Duration
	callback func()
	timer    *time.Timer
	stopped  bool
}

// New creates a debounce timer that will invoke callback after duration of
// inactivity. The timer does not start until Reset is called.
// Panics if duration is non-positive or callback is nil.
func New(duration time.Duration, callback func()) *Timer {
	if duration <= 0 {
		panic("debouncetimer: duration must be positive")
	}
	if callback == nil {
		panic("debouncetimer: callback must not be nil")
	}
	return &Timer{
		duration: duration,
		callback: callback,
	}
}

// Reset starts the timer or restarts the countdown if already running.
// After the duration elapses without another Reset, the callback fires.
// Reset is a no-op after Stop has been called.
func (t *Timer) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return
	}

	if t.timer != nil {
		t.timer.Stop()
	}

	t.timer = time.AfterFunc(t.duration, t.callback)
}

// Stop permanently cancels the timer. The callback will not fire after Stop
// returns. Stop is safe to call multiple times and after the callback has fired.
func (t *Timer) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.stopped = true
	if t.timer != nil {
		t.timer.Stop()
	}
}
