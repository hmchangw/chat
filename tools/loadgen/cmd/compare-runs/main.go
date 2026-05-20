// compare-runs reads histograms.hlog from two artifact bundles, deserializes
// the per-(scenario, kind, phase) HDR snapshots, and prints a Markdown
// comparison table.
//
// Usage: compare-runs <oldRunPath> <newRunPath>
//
// Each path is expected to be a directory like runs/<run_id>/ containing a
// histograms.hlog file (JSON map of "scenario|kind|phase" -> *hdr.Snapshot).
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: compare-runs <oldRunPath> <newRunPath>")
		os.Exit(2)
	}
	oldPath, newPath := os.Args[1], os.Args[2]

	oldHists, err := readHistograms(filepath.Join(oldPath, "histograms.hlog"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read old histograms: %v\n", err)
		os.Exit(1)
	}
	newHists, err := readHistograms(filepath.Join(newPath, "histograms.hlog"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read new histograms: %v\n", err)
		os.Exit(1)
	}

	// Collect all keys present in either.
	keys := map[string]struct{}{}
	for k := range oldHists {
		keys[k] = struct{}{}
	}
	for k := range newHists {
		keys[k] = struct{}{}
	}
	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	// Print Markdown table.
	fmt.Printf("# Run comparison\n\n")
	fmt.Printf("**Old:** `%s`\n**New:** `%s`\n\n", oldPath, newPath)
	fmt.Println("| Cell | Old p50 | New p50 | Δp50 | Old p99 | New p99 | Δp99 |")
	fmt.Println("|------|---------|---------|------|---------|---------|------|")

	for _, k := range sortedKeys {
		oldP50, oldP99 := quantiles(oldHists[k])
		newP50, newP99 := quantiles(newHists[k])
		dp50 := pctDelta(oldP50, newP50)
		dp99 := pctDelta(oldP99, newP99)
		fmt.Printf("| %s | %s | %s | %s | %s | %s | %s |\n",
			k,
			fmtMs(oldP50), fmtMs(newP50), fmtDelta(dp50),
			fmtMs(oldP99), fmtMs(newP99), fmtDelta(dp99),
		)
	}
}

func readHistograms(path string) (map[string]*hdr.Snapshot, error) {
	// #nosec G304 G703 -- path is operator-supplied via positional CLI arg to compare-runs; tool runs under the operator's own UID with no privileged file system access
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var raw map[string]*hdr.Snapshot
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return raw, nil
}

func quantiles(snap *hdr.Snapshot) (p50, p99 int64) {
	if snap == nil {
		return 0, 0
	}
	h := hdr.Import(snap)
	return h.ValueAtQuantile(50), h.ValueAtQuantile(99)
}

func pctDelta(old, new int64) float64 {
	if old == 0 {
		if new == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return 100.0 * (float64(new) - float64(old)) / float64(old)
}

func fmtMs(ns int64) string {
	if ns == 0 {
		return "—"
	}
	return fmt.Sprintf("%.2fms", float64(ns)/1e6)
}

func fmtDelta(pct float64) string {
	if math.IsInf(pct, 1) {
		return "(new)"
	}
	sign := "+"
	if pct < 0 {
		sign = ""
	}
	return fmt.Sprintf("%s%.1f%%", sign, pct)
}
