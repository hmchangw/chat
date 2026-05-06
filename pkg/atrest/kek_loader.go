package atrest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

// KEKLoader exposes the in-memory key set parsed from the secret file.
type KEKLoader interface {
	// Current returns the active KEK version and its 32-byte key.
	Current() (version int, key []byte)
	// ByVersion looks up a KEK by version. ok is false if absent.
	ByVersion(v int) (key []byte, ok bool)
	// Close signals the background reload goroutine to stop. It does NOT
	// block until the goroutine has exited; callers that need to ensure
	// no further file reads after Close should arrange shutdown via
	// context cancellation in their service main.
	Close() error
}

// fileKEKLoader is the file-backed implementation.
type fileKEKLoader struct {
	path string

	mu      sync.RWMutex
	keys    map[int][]byte
	current int
	modTime time.Time

	closeOnce sync.Once
	stop      chan struct{}
}

// NewFileKEKLoader reads and validates path, then starts a background
// goroutine that re-reads the file every 30 seconds. Reload failures
// retain the previous in-memory key set.
func NewFileKEKLoader(path string) (KEKLoader, error) {
	return newFileKEKLoaderWithInterval(path, 30*time.Second)
}

func newFileKEKLoaderWithInterval(path string, interval time.Duration) (KEKLoader, error) {
	keys, current, err := loadKEKFile(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat KEK file: %w", err)
	}
	l := &fileKEKLoader{
		path:    path,
		keys:    keys,
		current: current,
		modTime: info.ModTime(),
		stop:    make(chan struct{}),
	}
	kekCurrentVersion.Set(float64(current))
	slog.Info("atrest: KEK loaded", "path", path, "current", current, "versions", len(keys))
	go l.reloadLoop(interval)
	return l, nil
}

func (l *fileKEKLoader) Current() (int, []byte) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.current, l.keys[l.current]
}

func (l *fileKEKLoader) ByVersion(v int) ([]byte, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	k, ok := l.keys[v]
	return k, ok
}

func (l *fileKEKLoader) Close() error {
	l.closeOnce.Do(func() { close(l.stop) })
	return nil
}

func (l *fileKEKLoader) reloadLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.maybeReload()
		}
	}
}

func (l *fileKEKLoader) maybeReload() {
	info, err := os.Stat(l.path)
	if err != nil {
		// File temporarily unreadable; retain prior state. Try again next tick.
		kekReloadCounter.WithLabelValues(resultError).Inc()
		slog.Error("atrest: KEK stat failed", "path", l.path, "err", err)
		return
	}
	l.mu.RLock()
	prev := l.modTime
	l.mu.RUnlock()
	if !info.ModTime().After(prev) {
		return
	}
	keys, current, err := loadKEKFile(l.path)
	if err != nil {
		// Validation failed; retain prior state.
		kekReloadCounter.WithLabelValues(resultError).Inc()
		slog.Error("atrest: KEK reload failed", "path", l.path, "err", err)
		return
	}
	l.mu.Lock()
	l.keys = keys
	l.current = current
	l.modTime = info.ModTime()
	l.mu.Unlock()
	kekReloadCounter.WithLabelValues(resultOK).Inc()
	kekCurrentVersion.Set(float64(current))
	slog.Info("atrest: KEK reloaded", "path", l.path, "current", current, "versions", len(keys))
}

// fileFormat mirrors the on-disk JSON schema.
type fileFormat struct {
	Current int               `json:"current"`
	Keys    map[string]string `json:"keys"`
}

func loadKEKFile(path string) (map[int][]byte, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("reading KEK file: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, 0, fmt.Errorf("%w: parse JSON: %w", ErrKEKFileInvalid, err)
	}
	if len(f.Keys) == 0 {
		return nil, 0, fmt.Errorf("%w: keys is empty", ErrKEKFileInvalid)
	}
	out := make(map[int][]byte, len(f.Keys))
	versions := make([]int, 0, len(f.Keys))
	for vstr, b64 := range f.Keys {
		v, err := strconv.Atoi(vstr)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: version %q is not an integer", ErrKEKFileInvalid, vstr)
		}
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: version %d: base64 decode: %w", ErrKEKFileInvalid, v, err)
		}
		if len(key) != 32 {
			return nil, 0, fmt.Errorf("%w: version %d: key is %d bytes, want 32", ErrKEKFileInvalid, v, len(key))
		}
		out[v] = key
		versions = append(versions, v)
	}
	if _, ok := out[f.Current]; !ok {
		sort.Ints(versions)
		return nil, 0, fmt.Errorf("%w: current=%d not present in keys (have %v)", ErrKEKFileInvalid, f.Current, versions)
	}
	return out, f.Current, nil
}
