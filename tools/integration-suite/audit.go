package integrationsuite

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
