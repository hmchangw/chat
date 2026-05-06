package atrest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
)

// KEKLoader exposes the in-memory key set parsed from the secret file.
type KEKLoader interface {
	// Current returns the active KEK version and its 32-byte key.
	Current() (version int, key []byte)
	// ByVersion looks up a KEK by version. ok is false if absent.
	ByVersion(v int) (key []byte, ok bool)
	// Close releases any resources.
	Close() error
}

// fileKEKLoader is the file-backed implementation. Reload is added in a
// later task; this version reads once at construction.
type fileKEKLoader struct {
	path string

	mu      sync.RWMutex
	keys    map[int][]byte
	current int

	closeOnce sync.Once
	stop      chan struct{}
}

// NewFileKEKLoader reads and validates path, returning a loader holding
// the parsed key set in memory. Returns ErrKEKFileInvalid on schema or
// content failure.
func NewFileKEKLoader(path string) (KEKLoader, error) {
	keys, current, err := loadKEKFile(path)
	if err != nil {
		return nil, err
	}
	return &fileKEKLoader{
		path:    path,
		keys:    keys,
		current: current,
		stop:    make(chan struct{}),
	}, nil
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
		return nil, 0, fmt.Errorf("%w: parse JSON: %v", ErrKEKFileInvalid, err)
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
			return nil, 0, fmt.Errorf("%w: version %d: base64 decode: %v", ErrKEKFileInvalid, v, err)
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
