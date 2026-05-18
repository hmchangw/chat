// tools/loadgen/recommend.go
//
// `loadgen recommend` subcommand — suggests a preset and flag values
// based on target RPS and duration (cross-phase Task X.1).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

func runRecommend(_ *config, args []string) int {
	return runRecommendTo(os.Stdout, args)
}

func runRecommendTo(w io.Writer, args []string) int {
	fs := flag.NewFlagSet("recommend", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	targetRPS := fs.Int("target-rps", 100, "target requests/sec")
	duration := fs.Duration("duration", 5*time.Minute, "intended run duration")
	abortSustain := fs.Duration("abort-sustain", 30*time.Second, "abort-watcher sustain window")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(w, "error: %v\n", err)
		return 2
	}

	// Heuristic preset pick.
	preset := "small"
	switch {
	case *targetRPS >= 1000:
		preset = "large"
	case *targetRPS >= 200:
		preset = "medium"
	case *targetRPS >= 50:
		preset = "realistic"
	}

	// Recommended abort-window-max-samples: 2 × peak_rps × max_sustain.
	abortWindow := 2 * (*targetRPS) * int(abortSustain.Seconds())

	fmt.Fprintf(w, "Recommendation for --target-rps=%d --duration=%v:\n", *targetRPS, *duration)
	fmt.Fprintf(w, "  preset:                   %s\n", preset)
	fmt.Fprintf(w, "  abort-window-max-samples: %d  (= 2 × %d rps × %.0fs sustain)\n",
		abortWindow, *targetRPS, abortSustain.Seconds())
	fmt.Fprintf(w, "\nExample invocation:\n")
	fmt.Fprintf(w, "  loadgen run --scenario=messaging-pipeline --preset=%s --rate=%d \\\n",
		preset, *targetRPS)
	fmt.Fprintf(w, "      --duration=%v --abort-window-max-samples=%d\n",
		*duration, abortWindow)

	// Memory hint: check free memory if available.
	if free, ok := readMemAvailableMB(); ok {
		est := estimatePresetMemoryMB(preset)
		fmt.Fprintf(w, "\nMemory check:\n")
		fmt.Fprintf(w, "  estimated for preset:     %d MB\n", est)
		fmt.Fprintf(w, "  available on this host:   %d MB\n", free)
		if free < est {
			fmt.Fprintf(w, "  WARNING: available < estimated; preset may swap or OOM\n")
		}
	}

	return 0
}

func estimatePresetMemoryMB(preset string) int {
	estimates := map[string]int{
		"small":         50,
		"realistic":     150,
		"medium":        200,
		"large":         500,
		"channel-heavy": 200,
		"dm-heavy":      150,
	}
	if v, ok := estimates[preset]; ok {
		return v
	}
	return 100
}

// readMemAvailableMB reads /proc/meminfo's MemAvailable and returns MB.
// Returns (0, false) on systems without /proc (e.g., macOS).
func readMemAvailableMB() (int, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	// Parse "MemAvailable: NNN kB" line.
	var kb int
	for _, line := range splitLines(string(data)) {
		if _, err := fmt.Sscanf(line, "MemAvailable: %d kB", &kb); err == nil {
			return kb / 1024, true
		}
	}
	return 0, false
}

func splitLines(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
