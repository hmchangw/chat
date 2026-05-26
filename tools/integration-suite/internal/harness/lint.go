package harness

import (
	"regexp"
	"strings"
)

var blindspotInFeatureRE = regexp.MustCompile(`@blindspot:([a-z0-9][a-z0-9-]*)`)
var blindspotInRegisterRE = regexp.MustCompile(`(?m)^##\s+([a-z0-9][a-z0-9-]*)\s*$`)

// ExtractBlindspotSlugs returns all @blindspot:<slug> slugs found in
// a feature file's text.
func ExtractBlindspotSlugs(featureText string) []string {
	return uniq(matchAll(blindspotInFeatureRE, featureText))
}

// ExtractRegisteredSlugs returns slugs declared as level-2 headings
// in the blindspots.md register.
func ExtractRegisteredSlugs(registerText string) []string {
	return uniq(matchAll(blindspotInRegisterRE, registerText))
}

// DiffSlugs returns (slugs in features but not register, slugs in
// register but not features).
func DiffSlugs(features, register []string) (missingInRegister, missingInFeatures []string) {
	fset := setOf(features)
	rset := setOf(register)
	for s := range fset {
		if !rset[s] {
			missingInRegister = append(missingInRegister, s)
		}
	}
	for s := range rset {
		if !fset[s] {
			missingInFeatures = append(missingInFeatures, s)
		}
	}
	return
}

func matchAll(re *regexp.Regexp, s string) []string {
	matches := re.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func setOf(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[s] = true
	}
	return m
}

// JoinLines is a tiny helper for the CLI to format slug lists.
func JoinLines(slugs []string) string {
	return "  - " + strings.Join(slugs, "\n  - ")
}
