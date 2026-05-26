// Command audit-tally reads a reviewer-filled audit checklist and
// appends a tally + confusion matrix to docs/integration-suite-v1/audit-log.md.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	is "github.com/hmchangw/chat/tools/integration-suite/internal/harness"
)

func main() {
	input := flag.String("in", "", "path to the audit checklist markdown (required)")
	logFile := flag.String("log", "docs/integration-suite-v1/audit-log.md", "path to audit log markdown")
	flag.Parse()
	if *input == "" {
		fmt.Fprintln(os.Stderr, "audit-tally: --in is required")
		os.Exit(2)
	}

	md, err := os.ReadFile(*input)
	if err != nil {
		slog.Error("audit-tally: read input", "err", err)
		os.Exit(2)
	}
	classes, err := is.ParseChecklistClasses(md)
	if err != nil {
		slog.Error("audit-tally: parse", "err", err)
		os.Exit(2)
	}

	runID := runIDFromFilename(filepath.Base(*input))
	matrix := is.Tally(classes)
	body := is.RenderTally(runID, matrix)

	entry := fmt.Sprintf("\n## %s (run %s)\n\n%s\n",
		time.Now().UTC().Format(time.RFC3339), runID, body)

	if err := appendToFile(*logFile, entry); err != nil {
		slog.Error("audit-tally: append log", "err", err)
		os.Exit(2)
	}
	fmt.Println(body)
}

func runIDFromFilename(name string) string {
	s := strings.TrimSuffix(strings.TrimPrefix(name, "audit-"), ".md")
	if s == "" {
		return "unknown"
	}
	return s
}

func appendToFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}
