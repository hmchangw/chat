package integrationsuite

import (
	"fmt"
	"sort"
	"strings"
)

// ScopeSummary aggregates pass/fail/blindspot counts for one status group.
type ScopeSummary struct {
	Passed         int
	Failed         int
	Blindspot      int
	FailureByClass map[Class]int
}

func (s ScopeSummary) Total() int { return s.Passed + s.Failed + s.Blindspot }

// ScorePct returns the conformance score for this scope as a percent.
// Returns 0.0 when there are no scenarios in the scope.
func (s ScopeSummary) ScorePct() float64 {
	t := s.Total()
	if t == 0 {
		return 0.0
	}
	return 100.0 * float64(s.Passed) / float64(t)
}

// FailureRow describes one failing or blindspot scenario for inclusion
// in the human summary.
type FailureRow struct {
	Status      Status
	FeatureFile string
	Line        int
	Name        string
	Class       Class
	TraceID     string
	Reason      string // populated for blindspots
}

// RunSummary is the input to RenderSummary.
type RunSummary struct {
	RunID    string
	StartISO string
	Duration string
	Approved ScopeSummary
	Draft    ScopeSummary
	Failures []FailureRow
	// Last audit (optional): set zero values to omit.
	LastAuditISO string
	AuditN       int
	AuditAccPct  float64
	AuditFPPct   float64
	AuditFNPct   float64
}

// RenderSummary returns the markdown body of last-run.md.
func RenderSummary(s *RunSummary) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Run:        %s   (runID %s)\n", s.StartISO, s.RunID)
	total := s.Approved.Total() + s.Draft.Total()
	fmt.Fprintf(&b, "Total:      %d scenarios\n", total)
	fmt.Fprintf(&b, "Duration:   %s\n\n", s.Duration)

	writeScope(&b, "APPROVED", s.Approved)
	b.WriteString("\n")
	writeScope(&b, "DRAFT", s.Draft)

	if s.LastAuditISO != "" {
		fmt.Fprintf(&b, "\nLast audit: %s (n=%d) — accuracy %.1f%%, FP %.1f%%, FN %.1f%%\n",
			s.LastAuditISO, s.AuditN, s.AuditAccPct, s.AuditFPPct, s.AuditFNPct)
	}

	failures := nonBlindspotFailures(s.Failures)
	if len(failures) > 0 {
		b.WriteString("\nFailures (behavior diverged from design)\n")
		for _, f := range failures {
			fmt.Fprintf(&b, "  [%s] %s:%d %q\n", strings.ToUpper(string(f.Status)), f.FeatureFile, f.Line, f.Name)
			fmt.Fprintf(&b, "    class: %s\n", f.Class)
			if f.TraceID != "" {
				fmt.Fprintf(&b, "    trace: %s\n", f.TraceID)
			}
		}
	}

	blindspots := blindspotFailures(s.Failures)
	if len(blindspots) > 0 {
		b.WriteString("\nBlindspots (undocumented behavior — design owes an answer)\n")
		for _, f := range blindspots {
			fmt.Fprintf(&b, "  [%s] %s: %s\n", strings.ToUpper(string(f.Status)), f.FeatureFile, f.Reason)
		}
	}

	return b.String()
}

func writeScope(b *strings.Builder, label string, s ScopeSummary) {
	fmt.Fprintf(b, "%s   %d scenarios\n", label, s.Total())
	fmt.Fprintf(b, "  Passed:        %d\n", s.Passed)
	fmt.Fprintf(b, "  Failed:        %d\n", s.Failed)
	if s.Failed > 0 {
		classes := sortedClasses(s.FailureByClass)
		for _, c := range classes {
			fmt.Fprintf(b, "    %-14s %d\n", string(c)+":", s.FailureByClass[c])
		}
	}
	fmt.Fprintf(b, "  Blindspot:     %d\n", s.Blindspot)
	fmt.Fprintf(b, "  Score:       %5.1f%%   (%d / %d)\n", s.ScorePct(), s.Passed, s.Total())
}

func sortedClasses(m map[Class]int) []Class {
	out := make([]Class, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

func nonBlindspotFailures(f []FailureRow) []FailureRow {
	var out []FailureRow
	for _, r := range f {
		if r.Reason == "" {
			out = append(out, r)
		}
	}
	return out
}

func blindspotFailures(f []FailureRow) []FailureRow {
	var out []FailureRow
	for _, r := range f {
		if r.Reason != "" {
			out = append(out, r)
		}
	}
	return out
}
