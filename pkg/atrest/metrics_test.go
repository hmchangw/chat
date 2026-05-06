package atrest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtrest_KEKReloadMetrics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 1,
		"keys": {"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
	}`), 0o600))

	before := testutil.ToFloat64(kekReloadCounter.WithLabelValues("ok"))

	l, err := newFileKEKLoaderWithInterval(path, 20*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = l.Close()
	})

	assert.Equal(t, float64(1), testutil.ToFloat64(kekCurrentVersion))

	// Sleep > 1s for filesystem mtime resolution.
	time.Sleep(1100 * time.Millisecond)

	require.NoError(t, os.WriteFile(path, []byte(`{
		"current": 2,
		"keys": {
			"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
			"2": "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="
		}
	}`), 0o600))

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(kekCurrentVersion) == 2 &&
			testutil.ToFloat64(kekReloadCounter.WithLabelValues("ok")) > before
	}, 3*time.Second, 30*time.Millisecond)
}
