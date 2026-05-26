package harness

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Coverage measures how many *documented behavior cases* (the catalog in
// docs/integration-suite-v1/coverage.md) have a passing scenario. It is a
// completeness signal, distinct from the pass/fail two-score (soundness)
// and the audit confusion matrix (classifier trust). Report-only.

var coversInFeatureRE = regexp.MustCompile(`@covers:([a-z0-9][a-z0-9-]*)`)
var coverageHeadingRE = regexp.MustCompile(`(?m)^##\s+([a-z0-9][a-z0-9-]*)\s*$`)
var coverageFieldRE = regexp.MustCompile(`^\*\*([A-Za-z]+):\*\*\s*(.+)$`)

// CoverageEntry is one documented behavior case from the register.
type CoverageEntry struct {
	Slug     string
	Service  string // grouping; defaults to slug prefix before first '-'
	Source   string // spec citation (file + section)
	Behavior string // one-line description
	Declared string // declared intent: covered | blindspot | todo (default todo)
}

// ParseCoverageRegister parses docs/integration-suite-v1/coverage.md.
// An entry is a level-2 heading (the slug) followed by **Key:** lines
// until the next heading.
func ParseCoverageRegister(md string) []CoverageEntry {
	locs := coverageHeadingRE.FindAllStringSubmatchIndex(md, -1)
	out := make([]CoverageEntry, 0, len(locs))
	for i, loc := range locs {
		slug := md[loc[2]:loc[3]]
		bodyStart := loc[1]
		bodyEnd := len(md)
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
		}
		e := CoverageEntry{Slug: slug, Declared: "todo"}
		for _, line := range strings.Split(md[bodyStart:bodyEnd], "\n") {
			m := coverageFieldRE.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			switch strings.ToLower(m[1]) {
			case "service":
				e.Service = strings.TrimSpace(m[2])
			case "source":
				e.Source = strings.TrimSpace(m[2])
			case "behavior":
				e.Behavior = strings.TrimSpace(m[2])
			case "status":
				e.Declared = strings.ToLower(strings.TrimSpace(m[2]))
			}
		}
		if e.Service == "" {
			if idx := strings.IndexByte(slug, '-'); idx > 0 {
				e.Service = slug[:idx]
			} else {
				e.Service = slug
			}
		}
		out = append(out, e)
	}
	return out
}

// ExtractCoversSlugs returns all @covers:<slug> slugs in a feature text.
func ExtractCoversSlugs(featureText string) []string {
	return uniq(matchAll(coversInFeatureRE, featureText))
}

// RegisteredCoverageSlugs returns the slugs declared in the register.
func RegisteredCoverageSlugs(registerText string) []string {
	return uniq(matchAll(coverageHeadingRE, registerText))
}

// DiffCoverage returns (slugs @covers'd in features but absent from the
// register, slugs in the register never @covers'd by any feature).
func DiffCoverage(featureSlugs, registerSlugs []string) (orphanTags, neverReferenced []string) {
	fset := setOf(featureSlugs)
	rset := setOf(registerSlugs)
	for s := range fset {
		if !rset[s] {
			orphanTags = append(orphanTags, s)
		}
	}
	for s := range rset {
		if !fset[s] {
			neverReferenced = append(neverReferenced, s)
		}
	}
	sort.Strings(orphanTags)
	sort.Strings(neverReferenced)
	return
}

// ServiceCoverage aggregates one service's catalog.
type ServiceCoverage struct {
	Service   string
	Covered   int
	KnownGap  int // declared blindspot — a recorded, accepted gap
	Uncovered int
}

func (s ServiceCoverage) Total() int { return s.Covered + s.KnownGap + s.Uncovered }

// PctCovered returns Covered/Total as a percent (0 when empty).
func (s ServiceCoverage) PctCovered() float64 {
	t := s.Total()
	if t == 0 {
		return 0
	}
	return 100.0 * float64(s.Covered) / float64(t)
}

// UncoveredCase is a documented case with no passing covering scenario
// and not declared a known gap.
type UncoveredCase struct {
	Service  string
	Slug     string
	Behavior string
}

// CoverageReport is the rendered-once result.
type CoverageReport struct {
	Services  []ServiceCoverage
	Uncovered []UncoveredCase
}

// ScoreCoverage decodes the cucumber JSON, treats a case as covered when
// at least one scenario tagged @covers:<slug> passed (no failed step and
// not a @blindspot scenario), and buckets every register entry.
func ScoreCoverage(cucumberDoc []byte, entries []CoverageEntry) (*CoverageReport, error) {
	var features []cukeFeature
	if len(cucumberDoc) > 0 {
		if err := json.Unmarshal(cucumberDoc, &features); err != nil {
			return nil, fmt.Errorf("coverage: parse cucumber: %w", err)
		}
	}

	coveredSlugs := map[string]bool{}
	for _, f := range features {
		for _, sc := range f.Elements {
			if sc.Type != "scenario" {
				continue
			}
			tags := make([]string, 0, len(sc.Tags))
			for _, t := range sc.Tags {
				tags = append(tags, t.Name)
			}
			failed, _ := scenarioOutcome(&sc)
			isBlindspot := len(BlindspotsFromTags(tags)) > 0
			passed := !failed && !isBlindspot
			if !passed {
				continue
			}
			for _, t := range tags {
				m := coversInFeatureRE.FindStringSubmatch(t)
				if m != nil {
					coveredSlugs[m[1]] = true
				}
			}
		}
	}

	bySvc := map[string]*ServiceCoverage{}
	order := []string{}
	report := &CoverageReport{}
	for _, e := range entries {
		sc := bySvc[e.Service]
		if sc == nil {
			sc = &ServiceCoverage{Service: e.Service}
			bySvc[e.Service] = sc
			order = append(order, e.Service)
		}
		switch {
		case coveredSlugs[e.Slug]:
			sc.Covered++
		case e.Declared == "blindspot":
			sc.KnownGap++
		default:
			sc.Uncovered++
			report.Uncovered = append(report.Uncovered, UncoveredCase{
				Service: e.Service, Slug: e.Slug, Behavior: e.Behavior,
			})
		}
	}
	sort.Strings(order)
	for _, svc := range order {
		report.Services = append(report.Services, *bySvc[svc])
	}
	sort.Slice(report.Uncovered, func(i, j int) bool {
		if report.Uncovered[i].Service != report.Uncovered[j].Service {
			return report.Uncovered[i].Service < report.Uncovered[j].Service
		}
		return report.Uncovered[i].Slug < report.Uncovered[j].Slug
	})
	return report, nil
}

// RenderCoverage formats the report-only coverage block appended to
// last-run.md (and printed by cmd/coverage).
func RenderCoverage(r *CoverageReport) string {
	var b strings.Builder
	b.WriteString("\nCOVERAGE (documented behavior cases — report-only)\n")
	if len(r.Services) == 0 {
		b.WriteString("  (no coverage.md register found)\n")
		return b.String()
	}
	var tc, tk, tu int
	for _, s := range r.Services {
		fmt.Fprintf(&b, "  %-12s covered %d/%d   known-gap %d   uncovered %d   %5.1f%%\n",
			s.Service, s.Covered, s.Total(), s.KnownGap, s.Uncovered, s.PctCovered())
		tc += s.Covered
		tk += s.KnownGap
		tu += s.Uncovered
	}
	tot := tc + tk + tu
	pct := 0.0
	if tot > 0 {
		pct = 100.0 * float64(tc) / float64(tot)
	}
	fmt.Fprintf(&b, "  %-12s covered %d/%d   known-gap %d   uncovered %d   %5.1f%%\n",
		"TOTAL", tc, tot, tk, tu, pct)
	if len(r.Uncovered) > 0 {
		b.WriteString("\n  Uncovered (documented, no passing @covers scenario, not a known gap):\n")
		for _, u := range r.Uncovered {
			fmt.Fprintf(&b, "    [%s] %s — %s\n", u.Service, u.Slug, u.Behavior)
		}
	}
	return b.String()
}
