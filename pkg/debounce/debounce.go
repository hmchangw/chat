package debounce

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const defaultBatchSize = 100

// Callback is invoked when the debounce timer expires for a key.
type Callback func(ctx context.Context, key string) error

// Config holds debounce tuning parameters.
type Config struct {
	Timeout           time.Duration // Debounce quiet period (e.g., 10s)
	PollInterval      time.Duration // How often to check for expired keys (e.g., 1s)
	ProcessingTimeout time.Duration // Claim lease duration (e.g., 30s)
	MaxRetries        int           // Retry attempts on callback failure (e.g., 3)
	InitialBackoff    time.Duration // First retry delay, doubles each attempt (e.g., 2s)
}

// Debouncer manages distributed debounce timers via a Valkey sorted set.
type Debouncer struct {
	adapter  Adapter
	callback Callback
	cfg      Config
	now      func() time.Time // for testing; defaults to time.Now
	stopCh   chan struct{}    // closed by Close() to signal Start() to stop
	once     sync.Once        // ensures stopCh is closed exactly once
	wg       sync.WaitGroup
}

// New creates a Debouncer. Returns an error if Config has invalid values.
func New(adapter Adapter, callback Callback, cfg Config) (*Debouncer, error) {
	if cfg.PollInterval <= 0 {
		return nil, errors.New("debounce: PollInterval must be positive")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("debounce: Timeout must be positive")
	}
	if cfg.ProcessingTimeout <= 0 {
		return nil, errors.New("debounce: ProcessingTimeout must be positive")
	}
	return &Debouncer{
		adapter:  adapter,
		callback: callback,
		cfg:      cfg,
		now:      time.Now,
		stopCh:   make(chan struct{}),
	}, nil
}

// Trigger resets the debounce timer for the given key.
func (d *Debouncer) Trigger(ctx context.Context, key string) error {
	deadline := d.now().Add(d.cfg.Timeout)
	return d.adapter.Trigger(ctx, key, deadline)
}

// Start begins the background poll loop. Blocks until ctx is cancelled or
// Close is called. The passed-in context is used for adapter and callback calls;
// Close signals the loop to stop via an internal channel.
func (d *Debouncer) Start(ctx context.Context) error {
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.stopCh:
			return nil
		case <-ticker.C:
			d.poll(ctx)
		}
	}
}

// Close stops the poll loop and waits for in-flight callbacks to finish.
// Safe to call multiple times and before Start.
func (d *Debouncer) Close() {
	d.once.Do(func() { close(d.stopCh) })
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d.cfg.ProcessingTimeout):
		slog.Warn("debounce close timed out waiting for in-flight callbacks")
	}
}

func (d *Debouncer) poll(ctx context.Context) {
	entries, err := d.adapter.Claim(ctx, d.now(), d.cfg.ProcessingTimeout, defaultBatchSize)
	if err != nil {
		slog.Error("debounce claim failed", "error", err)
		return
	}
	for _, entry := range entries {
		d.wg.Add(1)
		go func(e ClaimedEntry) {
			defer d.wg.Done()
			d.processEntry(ctx, e)
		}(entry)
	}
}

func (d *Debouncer) processEntry(ctx context.Context, entry ClaimedEntry) {
	backoff := d.cfg.InitialBackoff
	for attempt := 0; attempt <= d.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff *= 2
			}
		}
		if err := d.callback(ctx, entry.Key); err != nil {
			slog.Warn("debounce callback failed",
				"key", entry.Key, "attempt", attempt+1, "error", err)
			continue
		}
		if err := d.adapter.Remove(ctx, entry.Key, entry.ClaimedScore); err != nil {
			slog.Error("debounce remove failed", "key", entry.Key, "error", err)
		}
		return
	}
	deadline := d.now().Add(d.cfg.Timeout)
	if err := d.adapter.Requeue(ctx, entry.Key, deadline); err != nil {
		slog.Error("debounce requeue failed", "key", entry.Key, "error", err)
	}
}
