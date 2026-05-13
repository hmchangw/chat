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
// EnvRedacted is pre-redacted by the caller (see RedactEnv). WriteBundle writes
// whatever it receives without further inspection. Task 1b.6 will add a more
// thorough allow-list on top of RedactEnv.
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create run dir %s: %w", dir, err)
	}

	if err := writeJSON(filepath.Join(dir, "summary.json"), b.Summary); err != nil {
		return err
	}
	// histograms.hlog: nil map serializes to JSON "null"; that's acceptable.
	if err := writeJSON(filepath.Join(dir, "histograms.hlog"), b.Histograms); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "settle.json"), b.Settle); err != nil {
		return err
	}
	if err := writeFlags(filepath.Join(dir, "flags.txt"), b.FlagsRaw); err != nil {
		return err
	}
	if err := writeEnv(filepath.Join(dir, "env.txt"), b.EnvRedacted); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "stdout.log"), b.StdoutLog); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "stderr.log"), b.StderrLog); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "metrics.prom"), b.MetricsProm); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "timeseries.jsonl"), b.TimeseriesJSONL); err != nil {
		return err
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

// secretSuffixes are key suffixes (upper-cased) that indicate a secret value.
// A key whose suffix matches any entry in this list is redacted.
var secretSuffixes = []string{
	"_TOKEN",
	"_KEY",
	"_PASSWORD",
	"_SECRET",
	"_CREDS_FILE",
	"_CREDS",
}

// secretExactKeys are key names (upper-cased) that should always be redacted
// regardless of suffix matching.
var secretExactKeys = map[string]bool{
	"AUTH_TOKEN":      true,
	"NATS_CREDS_FILE": true,
}

// RedactEnv returns a copy of env with secret values replaced by "***".
// A value is redacted when its key (case-insensitive):
//   - matches one of the exact keys in secretExactKeys, or
//   - ends with one of the secretSuffixes.
//
// All other values are passed through unchanged. The caller may pass an
// already-redacted map (e.g. from a previous RedactEnv call); "***" values
// are preserved without double-redaction.
//
// Task 1b.6 will augment this with a more thorough allow-list. For now,
// this covers the most obvious secret categories.
func RedactEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		upper := strings.ToUpper(k)
		if shouldRedact(upper) {
			out[k] = "***"
		} else {
			out[k] = v
		}
	}
	return out
}

func shouldRedact(upperKey string) bool {
	if secretExactKeys[upperKey] {
		return true
	}
	for _, suffix := range secretSuffixes {
		if strings.HasSuffix(upperKey, suffix) {
			return true
		}
	}
	return false
}
