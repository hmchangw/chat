package atrest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFileKEKLoader_Valid(t *testing.T) {
	l, err := NewFileKEKLoader(filepath.Join("testdata", "valid_keks.json"), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	ver, key := l.Current()
	assert.Equal(t, 2, ver)
	assert.Len(t, key, 32)

	k1, ok := l.ByVersion(1)
	assert.True(t, ok)
	assert.Len(t, k1, 32)

	_, ok = l.ByVersion(99)
	assert.False(t, ok)
}

func TestNewFileKEKLoader_ShortKey(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "bad_keks_short_key.json"), 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKFileInvalid))
}

func TestNewFileKEKLoader_MissingCurrent(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "bad_keks_missing_current.json"), 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKFileInvalid))
}

func TestNewFileKEKLoader_FileMissing(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "does_not_exist.json"), 0)
	require.Error(t, err)
}

func TestNewFileKEKLoader_EmptyKeys(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "bad_keks_empty.json"), 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKFileInvalid))
}

func TestNewFileKEKLoader_NonIntegerVersion(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "bad_keks_noninteger_version.json"), 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKFileInvalid))
}

func TestFileKEKLoader_HotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keks.json")

	// Initial file: current=1.
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 1,
		"keys": {"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
	}`), 0o600))

	l, err := newFileKEKLoaderWithInterval(path, 20*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	ver, _ := l.Current()
	assert.Equal(t, 1, ver)

	// Sleep briefly so the new file's mod-time is strictly after the
	// loader's recorded mod-time on filesystems with second-level
	// resolution.
	time.Sleep(1100 * time.Millisecond)

	// Rewrite with current=2.
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 2,
		"keys": {
			"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
			"2": "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="
		}
	}`), 0o600))

	require.Eventually(t, func() bool {
		v, _ := l.Current()
		return v == 2
	}, 3*time.Second, 30*time.Millisecond)
}

func TestFileKEKLoader_BadReloadKeepsPrior(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 1,
		"keys": {"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
	}`), 0o600))

	l, err := newFileKEKLoaderWithInterval(path, 20*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	time.Sleep(1100 * time.Millisecond)

	// Rewrite with malformed JSON.
	require.NoError(t, os.WriteFile(path, []byte(`{ not json`), 0o600))

	// Wait long enough for at least several reload ticks.
	time.Sleep(150 * time.Millisecond)

	ver, key := l.Current()
	assert.Equal(t, 1, ver)
	assert.Len(t, key, 32)
}
