package atrest

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFileKEKLoader_Valid(t *testing.T) {
	l, err := NewFileKEKLoader(filepath.Join("testdata", "valid_keks.json"))
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
	_, err := NewFileKEKLoader(filepath.Join("testdata", "bad_keks_short_key.json"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKFileInvalid))
}

func TestNewFileKEKLoader_MissingCurrent(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "bad_keks_missing_current.json"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKFileInvalid))
}

func TestNewFileKEKLoader_FileMissing(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "does_not_exist.json"))
	require.Error(t, err)
}

func TestNewFileKEKLoader_EmptyKeys(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "bad_keks_empty.json"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKFileInvalid))
}

func TestNewFileKEKLoader_NonIntegerVersion(t *testing.T) {
	_, err := NewFileKEKLoader(filepath.Join("testdata", "bad_keks_noninteger_version.json"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKEKFileInvalid))
}
