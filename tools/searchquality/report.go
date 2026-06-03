package main

import (
	"fmt"
	"sort"
	"strings"
)

// LangResult holds the aggregated per-language quality metrics comparing the
// blind (encrypted) index against the plaintext control index C.
type LangResult struct {
	Lang string
	// Recall is the mean RecallAtK(relevant, blindRanked, K) over the
	// language's queries — how many known-relevant docs the blind index
	// still surfaces in its top K.
	Recall float64
	// Jaccard is the mean set overlap of the top-K id lists (C vs blind).
	Jaccard float64
	// RBO is the mean rank-biased overlap of the full ranked id lists
	// (C vs blind) at the configured persistence p.
	RBO float64
	// ParityDivergences counts corpus docs in this language whose Go-side
	// msganalyzer.Analyze token stream did not match ES `_analyze`.
	ParityDivergences int
	// ParityExamples holds a few human-readable divergence samples.
	ParityExamples []string
	// Queries is the number of queries aggregated for this language.
	Queries int
}

// GatePassed reports whether the language meets the quality gate: mean
// recall@K >= recallGate AND mean RBO >= rboGate. Parity divergences are
// reported but do NOT fail the gate (they are diagnostic).
func (r *LangResult) GatePassed(recallGate, rboGate float64) bool {
	return r.Recall >= recallGate && r.RBO >= rboGate
}

// Report is the full quality-harness result across all languages.
type Report struct {
	K          int
	P          float64
	RecallGate float64
	RBOGate    float64
	Langs      []LangResult
}

// OverallPassed reports whether every language passed the gate.
func (rep *Report) OverallPassed() bool {
	for i := range rep.Langs {
		if !rep.Langs[i].GatePassed(rep.RecallGate, rep.RBOGate) {
			return false
		}
	}
	return len(rep.Langs) > 0
}

// renderReport renders the report as Markdown: a per-language metrics table
// plus the gate verdict and a parity-divergence appendix. Languages are sorted
// by name so the output is deterministic.
func renderReport(rep Report) string {
	langs := make([]LangResult, len(rep.Langs))
	copy(langs, rep.Langs)
	sort.Slice(langs, func(i, j int) bool { return langs[i].Lang < langs[j].Lang })

	var b strings.Builder
	b.WriteString("# Encrypted Search Quality Report\n\n")
	fmt.Fprintf(&b, "Comparing the blind (encrypted) index against plaintext control index C.\n\n")
	fmt.Fprintf(&b, "- K (top-K cutoff): %d\n", rep.K)
	fmt.Fprintf(&b, "- RBO persistence p: %.2f\n", rep.P)
	fmt.Fprintf(&b, "- Gate: recall@%d >= %.2f AND RBO >= %.2f per language\n\n", rep.K, rep.RecallGate, rep.RBOGate)

	b.WriteString("## Per-language metrics\n\n")
	fmt.Fprintf(&b, "| Language | Queries | Recall@%d | Jaccard | RBO | Parity divergences | Gate |\n", rep.K)
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for i := range langs {
		l := langs[i]
		verdict := "FAIL"
		if langs[i].GatePassed(rep.RecallGate, rep.RBOGate) {
			verdict = "PASS"
		}
		fmt.Fprintf(&b, "| %s | %d | %.3f | %.3f | %.3f | %d | %s |\n",
			l.Lang, l.Queries, l.Recall, l.Jaccard, l.RBO, l.ParityDivergences, verdict)
	}
	b.WriteString("\n")

	overall := "FAIL"
	if rep.OverallPassed() {
		overall = "PASS"
	}
	fmt.Fprintf(&b, "## Gate verdict: %s\n\n", overall)

	b.WriteString("## Parity divergences (msganalyzer.Analyze vs ES _analyze)\n\n")
	anyDivergence := false
	for _, l := range langs {
		if l.ParityDivergences == 0 {
			continue
		}
		anyDivergence = true
		fmt.Fprintf(&b, "### %s — %d divergent doc(s)\n\n", l.Lang, l.ParityDivergences)
		for _, ex := range l.ParityExamples {
			fmt.Fprintf(&b, "- %s\n", ex)
		}
		b.WriteString("\n")
	}
	if !anyDivergence {
		b.WriteString("No divergences detected: the Go analyzer matched ES `_analyze` on every corpus doc.\n")
	}
	return b.String()
}
