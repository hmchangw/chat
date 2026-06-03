package main

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSearchArm(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{"C", "C", false},
		{"A", "A", false},
		{"B", "B", false},
		{"c", "C", false}, // case-insensitive
		{"a", "A", false},
		{"", "", true},
		{"D", "", true},
		{"CA", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseSearchArm(tc.in)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRunSearchSustained_BadArm(t *testing.T) {
	cfg := &config{SiteID: "site-a"}
	// Bad --arm must fail flag validation before any NATS connect.
	code := runSearchSustained(t.Context(), cfg, []string{"--preset", "search-small", "--arm", "Z"})
	assert.Equal(t, 2, code)
}

func TestRunSearchSustained_MissingPreset(t *testing.T) {
	cfg := &config{SiteID: "site-a"}
	code := runSearchSustained(t.Context(), cfg, []string{"--arm", "C"})
	assert.Equal(t, 2, code)
}

func TestRunSearchSustained_UnknownPreset(t *testing.T) {
	cfg := &config{SiteID: "site-a"}
	code := runSearchSustained(t.Context(), cfg, []string{"--preset", "nope", "--arm", "C"})
	assert.Equal(t, 2, code)
}

func TestSearchFixturePreset(t *testing.T) {
	assert.Equal(t, "history-small", searchFixturePreset("search-small"))
	assert.Equal(t, "history-medium", searchFixturePreset("search-medium"))
	assert.Equal(t, "history-large", searchFixturePreset("search-large"))
	assert.Equal(t, "history-small", searchFixturePreset("unknown"))
}

func TestPrintSearchSummary_RendersArms(t *testing.T) {
	m := NewMetrics()
	c := NewSearchCollector(m, "search-small")
	now := time.Unix(100, 0)
	c.Record("A", 5*time.Millisecond, now, nil)
	c.Record("A", 7*time.Millisecond, now, nil)
	c.Record("A", 0, now, assert.AnError)

	p, ok := BuiltinSearchPreset("search-small")
	require.True(t, ok)
	var buf bytes.Buffer
	require.NoError(t, printSearchSummary(&buf, "site-a", &p, "A", 42, 20, 50*time.Second, c))
	out := buf.String()
	assert.Contains(t, out, "search-sustained")
	assert.Contains(t, out, "search-small")
	assert.Contains(t, out, "arm: A")
	assert.Contains(t, out, "site-a")
}

func TestWriteSearchCSVFile_EmitsPerArmRows(t *testing.T) {
	m := NewMetrics()
	c := NewSearchCollector(m, "search-small")
	now := time.Unix(100, 0)
	c.Record("C", 5*time.Millisecond, now, nil)
	c.Record("C", 7*time.Millisecond, now, nil)
	c.Record("A", 9*time.Millisecond, now, nil)

	path := filepath.Join(t.TempDir(), "search.csv")
	require.NoError(t, writeSearchCSVFile(path, c))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	require.NoError(t, err)

	// Header + 3 sample rows.
	require.Len(t, rows, 4)
	assert.Equal(t, []string{"timestamp_ns", "request_id", "metric", "latency_ns"}, rows[0])

	// Metric column carries the arm; both C and A appear.
	metrics := map[string]int{}
	for _, r := range rows[1:] {
		metrics[r[2]]++
	}
	assert.Equal(t, 2, metrics["C"])
	assert.Equal(t, 1, metrics["A"])
}
