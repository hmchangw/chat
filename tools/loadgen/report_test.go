package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failWriter returns an error on the first write.
type failWriter struct{ called bool }

func (f *failWriter) Write(p []byte) (int, error) {
	if !f.called {
		f.called = true
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func TestPercentiles_FixedSet(t *testing.T) {
	// 100 sorted values: 1ms..100ms
	samples := make([]time.Duration, 100)
	for i := range samples {
		samples[i] = time.Duration(i+1) * time.Millisecond
	}
	p := ComputePercentiles(samples)
	assert.Equal(t, 50*time.Millisecond, p.P50)
	assert.Equal(t, 95*time.Millisecond, p.P95)
	assert.Equal(t, 99*time.Millisecond, p.P99)
	assert.Equal(t, 100*time.Millisecond, p.Max)
}

func TestPercentiles_Empty(t *testing.T) {
	p := ComputePercentiles(nil)
	assert.Zero(t, p.P50)
	assert.Zero(t, p.P95)
	assert.Zero(t, p.P99)
	assert.Zero(t, p.Max)
}

func TestPrintSummary_ContainsKeyFields(t *testing.T) {
	var buf bytes.Buffer
	s := Summary{
		Preset: "medium", Seed: 42, Site: "site-local",
		TargetRate: 500, ActualRate: 499.8,
		Duration: 60 * time.Second, Warmup: 10 * time.Second,
		Inject: "frontdoor", Sent: 25000,
	}
	require.NoError(t, PrintSummary(&buf, &s))
	out := buf.String()
	for _, want := range []string{
		"preset: medium", "seed: 42", "site: site-local",
		"sent (total):", "sent (measured):", "25000", "inject: frontdoor",
	} {
		assert.True(t, strings.Contains(out, want), "summary missing %q; got:\n%s", want, out)
	}
}

func TestWriteCSV_OneRowPerSample(t *testing.T) {
	var buf bytes.Buffer
	rows := []CSVSample{
		{RowIndex: 1, RequestID: "r1", Metric: "E1", LatencyNs: 2_100_000},
		{RowIndex: 2, RequestID: "r1", Metric: "E2", LatencyNs: 8_700_000},
	}
	require.NoError(t, WriteCSV(&buf, rows))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 3) // header + 2 rows
	assert.Equal(t, "row_index,request_id,metric,latency_ns", lines[0])
	assert.Equal(t, "1,r1,E1,2100000", lines[1])
	assert.Equal(t, "2,r1,E2,8700000", lines[2])
}

func TestPrintSummary_WithConsumers(t *testing.T) {
	var buf bytes.Buffer
	s := Summary{
		Preset: "heavy", Seed: 1, Site: "site-a",
		TargetRate: 1000, ActualRate: 998.5,
		Duration: 120 * time.Second, Warmup: 20 * time.Second,
		Inject: "gateway",
		Consumers: []ConsumerStat{
			{
				Stream: "MESSAGES_CANONICAL_site-a", Durable: "message-worker",
				MinPending: 0, PeakPending: 150, FinalPending: 2,
				PeakAckPending: 10, Redelivered: 1,
			},
		},
	}
	require.NoError(t, PrintSummary(&buf, &s))
	out := buf.String()
	assert.True(t, strings.Contains(out, "consumer lag"), "missing consumer lag header; got:\n%s", out)
	assert.True(t, strings.Contains(out, "message-worker"), "missing durable name; got:\n%s", out)
	assert.True(t, strings.Contains(out, "150"), "missing peak pending; got:\n%s", out)
}

func TestWriteCSV_Empty(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteCSV(&buf, nil))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 1) // header only
	assert.Equal(t, "row_index,request_id,metric,latency_ns", lines[0])
}

func TestWriteCSV_WriterError(t *testing.T) {
	// failWriter errors on the first write; csv buffers internally so the
	// error surfaces via cw.Error() after Flush, not from cw.Write directly.
	err := WriteCSV(&failWriter{}, []CSVSample{})
	require.Error(t, err)
}

func TestWriteCSV_RowWriteError(t *testing.T) {
	// Use a writer that succeeds the first write (header) but then a pipe
	// that we close, so the row write fails.
	pr, pw := io.Pipe()
	pw.Close() // close write end immediately so subsequent writes fail

	// Drain the reader so csv can flush the header without blocking.
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		_, _ = io.Copy(io.Discard, pr)
	}()

	rows := []CSVSample{
		{RowIndex: 1, RequestID: "r1", Metric: "E1", LatencyNs: 100},
	}
	err := WriteCSV(pw, rows)
	<-doneCh
	require.Error(t, err)
}

func TestPrintSummary_WithRequestStats(t *testing.T) {
	var buf bytes.Buffer
	s := Summary{
		Preset: "history-read", Seed: 42, Site: "site-local",
		TargetRate: 200, ActualRate: 199.5,
		Duration: 30 * time.Second, Warmup: 10 * time.Second,
		Inject: "frontdoor",
		Requests: []RequestStat{
			{
				Scenario: "history", Kind: "load_history",
				Count: 1200, Errors: 0,
				Latency: Percentiles{
					P50: 4 * time.Millisecond,
					P95: 18 * time.Millisecond,
					P99: 42 * time.Millisecond,
					Max: 95 * time.Millisecond,
				},
			},
			{
				Scenario: "history", Kind: "get_message_by_id",
				Count: 400, Errors: 3,
				Latency: Percentiles{
					P50: 1 * time.Millisecond,
					P95: 3 * time.Millisecond,
					P99: 8 * time.Millisecond,
					Max: 12 * time.Millisecond,
				},
			},
		},
	}
	require.NoError(t, PrintSummary(&buf, &s))
	out := buf.String()
	assert.Contains(t, out, "request latency", "summary missing request-latency section")
	assert.Contains(t, out, "load_history")
	assert.Contains(t, out, "get_message_by_id")
	assert.Contains(t, out, "1200")
	// Error count must surface (3 above).
	assert.Contains(t, out, "3")
}

func TestPrintSummary_NoRequestStats_NoSection(t *testing.T) {
	var buf bytes.Buffer
	s := Summary{
		Preset:     "small",
		Seed:       1,
		Site:       "site-a",
		TargetRate: 100, ActualRate: 99.9,
		Duration: 30 * time.Second, Warmup: 10 * time.Second,
		Inject: "frontdoor",
	}
	require.NoError(t, PrintSummary(&buf, &s))
	assert.NotContains(t, buf.String(), "request latency",
		"messaging-pipeline runs should not render the request-latency section")
}

func TestWriteCSV_RequestSamples(t *testing.T) {
	var buf bytes.Buffer
	rows := []CSVSample{
		{RowIndex: 1, RequestID: "r1", Metric: "history.load_history", LatencyNs: 4_200_000},
		{RowIndex: 2, RequestID: "r2", Metric: "search.search_messages", LatencyNs: 3_700_000},
	}
	require.NoError(t, WriteCSV(&buf, rows))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 3) // header + 2 rows
	assert.Equal(t, "1,r1,history.load_history,4200000", lines[1])
	assert.Equal(t, "2,r2,search.search_messages,3700000", lines[2])
}

func TestDetermineExitCode(t *testing.T) {
	cases := []struct {
		name         string
		sent         int
		errs         int
		wantExitCode int
	}{
		{"zero errors", 10000, 0, 0},
		{"under tolerance", 10000, 9, 0},        // 0.09% < 0.1%
		{"at tolerance boundary", 10000, 10, 0}, // exactly 0.1%: pass
		{"over tolerance", 10000, 11, 1},        // 0.11% > 0.1%
		{"no sends no errors", 0, 0, 0},
		{"no sends - any error fails", 0, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantExitCode, DetermineExitCode(tc.sent, tc.errs))
		})
	}
}
