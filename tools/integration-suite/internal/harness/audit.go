package harness

import (
	"fmt"
	"math/rand"
	"strings"
)

// AuditRow is one approved-scenario outcome eligible for sampling.
type AuditRow struct {
	FeatureFile string
	Line        int
	Name        string
	Outcome     string // "Passed" or "Failed"
}

// SampleApproved selects up to n rows from approved without replacement
// using a deterministic RNG seed (so re-runs are reproducible).
func SampleApproved(approved []AuditRow, n int, seed int64) []AuditRow {
	if n >= len(approved) {
		return approved
	}
	r := rand.New(rand.NewSource(seed))
	idx := r.Perm(len(approved))[:n]
	out := make([]AuditRow, 0, n)
	for _, i := range idx {
		out = append(out, approved[i])
	}
	return out
}

// RenderAuditChecklist returns the markdown checklist for human review.
// Two empty columns at the end are filled in by the reviewer:
// "Reviewer says" and "Class" (TP/TN/FP/FN).
func RenderAuditChecklist(runID string, sampled []AuditRow) string {
	if len(sampled) == 0 {
		return fmt.Sprintf("# Suite Audit — run %s\n\nno approved scenarios sampled.\n", runID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Suite Audit — run %s\n\n", runID)
	fmt.Fprintf(&b, "Sampled %d approved scenarios. Reviewer fills the last two columns.\n\n", len(sampled))
	b.WriteString("| Scenario | Outcome | Reviewer says | Class |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, r := range sampled {
		fmt.Fprintf(&b, "| %s:%d %s | %s |  |  |\n", r.FeatureFile, r.Line, r.Name, r.Outcome)
	}
	b.WriteString("\nFill Class as one of: TP, TN, FP, FN.\n")
	b.WriteString("Then run `make integration-suite-audit-tally`.\n")
	return b.String()
}

// AuditClass is one of TP / TN / FP / FN.
type AuditClass string

const (
	ClassTP AuditClass = "TP"
	ClassTN AuditClass = "TN"
	ClassFP AuditClass = "FP"
	ClassFN AuditClass = "FN"
)

// ConfusionMatrix aggregates TP/TN/FP/FN counts.
type ConfusionMatrix struct {
	TP, TN, FP, FN int
}

// Tally computes the matrix from a flat list of classifications.
func Tally(rows []AuditClass) ConfusionMatrix {
	var m ConfusionMatrix
	for _, c := range rows {
		switch c {
		case ClassTP:
			m.TP++
		case ClassTN:
			m.TN++
		case ClassFP:
			m.FP++
		case ClassFN:
			m.FN++
		}
	}
	return m
}

// Total returns the sample size.
func (m ConfusionMatrix) Total() int { return m.TP + m.TN + m.FP + m.FN }

// AccuracyPct returns (TP+TN)/total as a percent. Zero when total is 0.
func (m ConfusionMatrix) AccuracyPct() float64 {
	t := m.Total()
	if t == 0 {
		return 0
	}
	return 100.0 * float64(m.TP+m.TN) / float64(t)
}

// FalsePositiveRatePct returns FP / (FP + TN) as a percent.
func (m ConfusionMatrix) FalsePositiveRatePct() float64 {
	d := m.FP + m.TN
	if d == 0 {
		return 0
	}
	return 100.0 * float64(m.FP) / float64(d)
}

// FalseNegativeRatePct returns FN / (FN + TP) as a percent.
func (m ConfusionMatrix) FalseNegativeRatePct() float64 {
	d := m.FN + m.TP
	if d == 0 {
		return 0
	}
	return 100.0 * float64(m.FN) / float64(d)
}

// RenderTally returns the markdown summary of a confusion matrix run.
func RenderTally(runID string, m ConfusionMatrix) string {
	return fmt.Sprintf(`# Audit Tally — run %s

Confusion matrix (n=%d):
              | actually correct | actually broken
test passed   |       TP=%d      |      FP=%d
test failed   |       FN=%d      |      TN=%d

Accuracy:                 %.1f%%
False-positive rate:      %.1f%%
False-negative rate:      %.1f%%
`, runID, m.Total(), m.TP, m.FP, m.FN, m.TN,
		m.AccuracyPct(), m.FalsePositiveRatePct(), m.FalseNegativeRatePct())
}

// ParseChecklistClasses extracts the Class column from a checklist
// markdown that the reviewer has filled in. Empty Class cells are
// skipped (sampled but un-classified rows do not count).
func ParseChecklistClasses(md []byte) ([]AuditClass, error) {
	lines := strings.Split(string(md), "\n")
	var out []AuditClass
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "|") {
			continue
		}
		cols := strings.Split(ln, "|")
		// Expect at least: | scenario | outcome | reviewer | class |
		if len(cols) < 6 {
			continue
		}
		raw := strings.TrimSpace(cols[4])
		switch raw {
		case "TP", "TN", "FP", "FN":
			out = append(out, AuditClass(raw))
		case "Class", "---", "":
			// header / separator / unfilled
		}
	}
	return out, nil
}
