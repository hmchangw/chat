package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
)

// ArtifactBundle holds everything that WriteBundle persists to runs/<run_id>/.
// Fields that are nil/empty produce empty (but present) artifact files — all 9
// files are always created so post-mortems can rely on the directory layout.
//
// EnvRedacted is pre-redacted by the caller (see RedactEnv in creds.go).
// WriteBundle writes whatever it receives without further inspection.
//
// StdoutLog / StderrLog are set to nil in Phase 1b.1; log-tee plumbing is
// deferred to a future Phase 1b task.
//
// TimeseriesJSONL is set to nil in Phase 1b.1; the per-second ticker
// integration is a Phase 1b stretch goal.
type ArtifactBundle struct {
	RunID           string
	Summary         Summary
	Histograms      map[string]*hdr.Snapshot
	Settle          SettleOutcome
	FlagsRaw        []string
	EnvRedacted     map[string]string
	StdoutLog       []byte
	StderrLog       []byte
	MetricsProm     []byte
	TimeseriesJSONL []byte
}

// WriteBundle persists all artifact files for bundle to rootDir/<bundle.RunID>/.
// The run directory is created if it does not exist. All 9 artifact files are
// always written — empty fields produce zero-byte (or valid-JSON-empty) files.
func WriteBundle(rootDir string, b *ArtifactBundle) error {
	dir := filepath.Join(rootDir, b.RunID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create run dir %s: %w", dir, err)
	}

	if err := writeJSON(filepath.Join(dir, "summary.json"), b.Summary); err != nil {
		return fmt.Errorf("write summary artifact: %w", err)
	}
	// histograms.hlog: HDR snapshots serialized as indented JSON.
	// NOTE: hdr.Snapshot.Counts is a dense ~11k-bucket array; expect ~80KB per histogram cell,
	// almost entirely zeros (one entry per HDR bucket). Non-zero bucket indices correspond to
	// recorded latency buckets per the HDR formula in github.com/HdrHistogram/hdrhistogram-go.
	// Interpretation requires the library. A more debuggable format (compressed or HDR native log)
	// is a follow-up — see CHANGES.md notes. Nil map serializes to JSON "null"; that's acceptable.
	if err := writeJSON(filepath.Join(dir, "histograms.hlog"), b.Histograms); err != nil {
		return fmt.Errorf("write histograms artifact: %w", err)
	}
	if err := writeJSON(filepath.Join(dir, "settle.json"), b.Settle); err != nil {
		return fmt.Errorf("write settle artifact: %w", err)
	}
	if err := writeFlags(filepath.Join(dir, "flags.txt"), b.FlagsRaw); err != nil {
		return fmt.Errorf("write flags artifact: %w", err)
	}
	if err := writeEnv(filepath.Join(dir, "env.txt"), b.EnvRedacted); err != nil {
		return fmt.Errorf("write env artifact: %w", err)
	}
	if err := writeFile(filepath.Join(dir, "stdout.log"), b.StdoutLog); err != nil {
		return fmt.Errorf("write stdout log: %w", err)
	}
	if err := writeFile(filepath.Join(dir, "stderr.log"), b.StderrLog); err != nil {
		return fmt.Errorf("write stderr log: %w", err)
	}
	if err := writeFile(filepath.Join(dir, "metrics.prom"), b.MetricsProm); err != nil {
		return fmt.Errorf("write metrics artifact: %w", err)
	}
	if err := writeFile(filepath.Join(dir, "timeseries.jsonl"), b.TimeseriesJSONL); err != nil {
		return fmt.Errorf("write timeseries artifact: %w", err)
	}
	return nil
}

// writeJSON marshals v as indented JSON and writes it to path with a trailing newline.
func writeJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	return writeFile(path, append(data, '\n'))
}

// writeFile writes data to path. A nil data slice writes an empty file.
func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// writeFlags writes each flag entry on its own line.
func writeFlags(path string, flags []string) error {
	var sb strings.Builder
	for _, f := range flags {
		sb.WriteString(f)
		sb.WriteByte('\n')
	}
	return writeFile(path, []byte(sb.String()))
}

// writeEnv writes key=value pairs sorted by key, one per line.
func writeEnv(path string, env map[string]string) error {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%s\n", k, env[k])
	}
	return writeFile(path, []byte(sb.String()))
}
