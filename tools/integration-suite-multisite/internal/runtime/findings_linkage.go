package runtime

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// FindingsIndex maps a scenario filename (basename, no extension) to
// the F-NNN id it demonstrates. Built from
// docs/integration-suite-multisite-findings.md by scanning each
// "## F-NNN — title" heading and its body for scenario YAML paths.
//
// Multiple scenarios under one finding are all mapped to the same id
// (a finding can have several demonstration scenarios).
type FindingsIndex map[string]string

var (
	// Headings: "## F-018 — title" or "## F-018  title". The em-dash
	// in the project's actual doc is U+2014.
	findingHeadingRe = regexp.MustCompile(`^##\s+(F-\d+)\b`)
	// Scenario references in finding bodies: a backticked path that
	// ends in a YAML basename under scenarios/drafts/. Captures the
	// basename (without .yaml). Allows:
	//   - optional tools/integration-suite-multisite/ prefix
	//   - zero or more subdirectories between drafts/ and the basename
	scenarioRefRe = regexp.MustCompile("`(?:tools/integration-suite-multisite/)?scenarios/drafts/(?:[^`]*?/)?([a-z0-9][a-z0-9_-]*)\\.yaml`")
)

// ParseFindings walks the findings doc and builds the scenario→F-NNN
// index. Lines outside any "## F-NNN" heading are ignored. When a
// scenario reference appears under a heading, it is recorded with that
// heading's F-NNN.
func ParseFindings(doc string) FindingsIndex {
	idx := FindingsIndex{}
	current := ""
	for _, line := range strings.Split(doc, "\n") {
		if m := findingHeadingRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		if current == "" {
			continue
		}
		for _, m := range scenarioRefRe.FindAllStringSubmatch(line, -1) {
			idx[m[1]] = current
		}
	}
	return idx
}

// scenarioBasename extracts the YAML filename without extension from
// a scenario SourcePath. Used to match against FindingsIndex keys.
func scenarioBasename(sourcePath string) string {
	if sourcePath == "" {
		return ""
	}
	base := filepath.Base(sourcePath)
	return strings.TrimSuffix(base, ".yaml")
}

// RenderWithFindings renders the run report with an additional
// section linking DRAFT fail scenarios to their F-NNN findings (and
// flagging unlinked DRAFT fails as Untriaged). Backward-compatible
// shape: when idx is empty/nil, the output matches Render(r).
func RenderWithFindings(r *RunReport, approvedOnly bool, idx FindingsIndex) string {
	body := render(r, approvedOnly)
	section := renderFindingsLinkage(r, idx)
	if section == "" {
		return body
	}
	return body + section
}

// renderFindingsLinkage builds the "Open findings" and "Untriaged
// DRAFT fails" sections. Returns "" when there's nothing to report.
func renderFindingsLinkage(r *RunReport, idx FindingsIndex) string {
	if idx == nil {
		return ""
	}
	type row struct {
		name    string
		finding string
	}
	var linked, untriaged []row
	for i := range r.Cases {
		c := &r.Cases[i]
		// Only DRAFT fails. APPROVED fails are CI regressions, not
		// "open findings"; PASS scenarios are not bugs.
		if c.Status == "approved" || c.Verdict.Outcome != "fail" {
			continue
		}
		base := scenarioBasename(c.SourcePath)
		if f, has := idx[base]; has {
			linked = append(linked, row{name: c.ScenarioName, finding: f})
		} else {
			untriaged = append(untriaged, row{name: c.ScenarioName})
		}
	}
	if len(linked) == 0 && len(untriaged) == 0 {
		return ""
	}

	var b strings.Builder
	if len(linked) > 0 {
		fmt.Fprintf(&b, "\n## Open findings (%d) — DRAFT fails linked to F-NNN\n\n",
			len(linked))
		b.WriteString("Each row below is a draft scenario whose failure documents an\n")
		b.WriteString("open finding in `docs/integration-suite-multisite-findings.md`.\n")
		b.WriteString("These are informational reds, not regressions. They flip to\n")
		b.WriteString("pass when the chat-app team ships the fix; promote to\n")
		b.WriteString("`status: approved` then.\n\n")
		for _, r := range linked {
			fmt.Fprintf(&b, "  %s  %s\n", r.finding, r.name)
		}
		b.WriteString("\n")
	}
	if len(untriaged) > 0 {
		fmt.Fprintf(&b, "## Untriaged DRAFT fails (%d) — not linked to any F-NNN\n\n",
			len(untriaged))
		b.WriteString("These DRAFT scenarios are failing but are not cited by any\n")
		b.WriteString("F-NNN finding in the findings doc. Either:\n")
		b.WriteString("  - the scenario is a new bug → file a F-NNN entry citing\n")
		b.WriteString("    its YAML path under \"Demonstrated by\", or\n")
		b.WriteString("  - the scenario is broken / stale → fix or remove it.\n")
		b.WriteString("Leaving an untriaged red erodes the \"reds = open findings\"\n")
		b.WriteString("contract from AUTHORING.md §Pass/fail semantics.\n\n")
		for _, r := range untriaged {
			fmt.Fprintf(&b, "  - %s\n", r.name)
		}
		b.WriteString("\n")
	}
	return b.String()
}
