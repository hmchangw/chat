package msganalyzer

import "regexp"

// splitRE mirrors the ES "underscore_preserving" pattern tokenizer: it splits
// on whitespace, common punctuation, brackets, quotes, and angle brackets, but
// deliberately NOT on underscores, hyphens, or dots.
var splitRE = regexp.MustCompile(`[\s,;!?()\[\]{}"'<>]+`)

// tokenizePattern splits text into tokens, dropping empty results produced by
// leading, trailing, or repeated delimiters.
func tokenizePattern(s string) []string {
	var out []string
	for _, t := range splitRE.Split(s, -1) {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
