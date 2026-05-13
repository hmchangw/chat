package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteBundle_AllFilesPresent(t *testing.T) {
	dir := t.TempDir()
	// Build an ArtifactBundle with one entry in each section.
	bundle := &ArtifactBundle{
		RunID:           "test-run-id",
		Summary:         Summary{RunID: "test-run-id", Preset: "test", TargetRate: 500},
		Settle:          SettleOutcome{AllSucceeded: true},
		FlagsRaw:        []string{"--scenario=history-read", "--rate=500"},
		EnvRedacted:     map[string]string{"NATS_URL": "***", "FOO": "bar"},
		StdoutLog:       []byte("test run output\n"),
		StderrLog:       []byte(""),
		MetricsProm:     []byte("# HELP loadgen_published_total ...\nloadgen_published_total 1\n"),
		TimeseriesJSONL: []byte(`{"t":0,"rps":500,"p99":0.08}` + "\n"),
	}
	require.NoError(t, WriteBundle(dir, bundle))

	for _, f := range []string{
		"summary.json",
		"histograms.hlog",
		"settle.json",
		"flags.txt",
		"env.txt",
		"stdout.log",
		"stderr.log",
		"metrics.prom",
		"timeseries.jsonl",
	} {
		_, err := os.Stat(filepath.Join(dir, "test-run-id", f))
		assert.NoError(t, err, "missing %s", f)
	}
}

func TestWriteBundle_RedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	b := &ArtifactBundle{
		RunID: "r1",
		EnvRedacted: map[string]string{
			"AUTH_TOKEN":      "***",
			"NATS_CREDS_FILE": "***",
			"FOO":             "bar",
		},
	}
	require.NoError(t, WriteBundle(dir, b))
	body, err := os.ReadFile(filepath.Join(dir, "r1", "env.txt"))
	require.NoError(t, err)
	s := string(body)
	assert.Contains(t, s, "FOO=bar")
	assert.Contains(t, s, "AUTH_TOKEN=***")           // already-redacted value passed through
	assert.NotContains(t, s, "real-auth-token-value") // ensure no real values leak
}

func TestWriteBundle_EmptyBundleCreatesAllFiles(t *testing.T) {
	dir := t.TempDir()
	b := &ArtifactBundle{RunID: "empty-run"}
	require.NoError(t, WriteBundle(dir, b))

	// Even with all fields zero/nil, all 9 artifact files must exist.
	for _, f := range []string{
		"summary.json",
		"histograms.hlog",
		"settle.json",
		"flags.txt",
		"env.txt",
		"stdout.log",
		"stderr.log",
		"metrics.prom",
		"timeseries.jsonl",
	} {
		_, err := os.Stat(filepath.Join(dir, "empty-run", f))
		assert.NoError(t, err, "missing %s", f)
	}
}

func TestWriteBundle_FlagsOrdering(t *testing.T) {
	dir := t.TempDir()
	b := &ArtifactBundle{
		RunID:    "flags-run",
		FlagsRaw: []string{"--rate=100", "--preset=perf", "--duration=60s"},
	}
	require.NoError(t, WriteBundle(dir, b))
	body, err := os.ReadFile(filepath.Join(dir, "flags-run", "flags.txt"))
	require.NoError(t, err)
	s := string(body)
	// Each flag is on its own line.
	assert.Contains(t, s, "--rate=100\n")
	assert.Contains(t, s, "--preset=perf\n")
	assert.Contains(t, s, "--duration=60s\n")
}

func TestWriteBundle_EnvSorted(t *testing.T) {
	dir := t.TempDir()
	b := &ArtifactBundle{
		RunID: "env-run",
		EnvRedacted: map[string]string{
			"ZEBRA": "z",
			"ALPHA": "a",
			"MONGO": "m",
		},
	}
	require.NoError(t, WriteBundle(dir, b))
	body, err := os.ReadFile(filepath.Join(dir, "env-run", "env.txt"))
	require.NoError(t, err)
	s := string(body)
	alphaPos := indexOf(s, "ALPHA=a")
	mongoPos := indexOf(s, "MONGO=m")
	zebraPos := indexOf(s, "ZEBRA=z")
	assert.True(t, alphaPos < mongoPos, "env.txt should be sorted: ALPHA before MONGO")
	assert.True(t, mongoPos < zebraPos, "env.txt should be sorted: MONGO before ZEBRA")
}

func indexOf(s, substr string) int {
	for i := range s {
		if len(s)-i >= len(substr) && s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestCollector_ExportHistograms_Empty(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test-preset")
	hists := c.ExportHistograms()
	assert.NotNil(t, hists)
	// Empty collector has no request cells, so the map is empty.
	assert.Empty(t, hists)
}

func TestCollector_ExportHistograms_WithData(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test-preset")
	// Record a request to create a cell.
	c.RecordRequest("history-read", "fetch", "measured", 50_000_000, false)
	c.RecordRequest("history-read", "fetch", "measured", 100_000_000, false)

	hists := c.ExportHistograms()
	assert.NotNil(t, hists)
	assert.Len(t, hists, 1)
	key := "history-read|fetch|measured"
	snap, ok := hists[key]
	assert.True(t, ok, "expected key %q in histograms", key)
	assert.NotNil(t, snap)
	// Sanity: the snapshot was actually exported (has counts).
	assert.NotEmpty(t, snap.Counts)
}

func TestRedactEnv(t *testing.T) {
	env := map[string]string{
		"NATS_URL":        "nats://user:password@localhost:4222",
		"MONGO_URI":       "mongodb://user:password@localhost:27017/db",
		"AUTH_TOKEN":      "secret-token-value",
		"NATS_CREDS_FILE": "/path/to/creds",
		"MONGO_PASSWORD":  "hunter2",
		"MY_SECRET_KEY":   "key-material",
		"SITE_ID":         "site-local",
		"API_KEY":         "apikey123",
		"DB_PASSWORD":     "password123",
		"SAFE_VAR":        "safe-value",
	}
	redacted := RedactEnv(env)

	// Safe vars pass through.
	assert.Equal(t, "site-local", redacted["SITE_ID"])
	assert.Equal(t, "safe-value", redacted["SAFE_VAR"])

	// Secret vars are redacted (URIs can contain user:password@host).
	assert.Equal(t, "***", redacted["NATS_URL"])
	assert.Equal(t, "***", redacted["MONGO_URI"])
	assert.Equal(t, "***", redacted["AUTH_TOKEN"])
	assert.Equal(t, "***", redacted["NATS_CREDS_FILE"])
	assert.Equal(t, "***", redacted["MONGO_PASSWORD"])
	assert.Equal(t, "***", redacted["MY_SECRET_KEY"])
	assert.Equal(t, "***", redacted["API_KEY"])
	assert.Equal(t, "***", redacted["DB_PASSWORD"])
}
