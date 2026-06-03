package msganalyzer

import (
	"html"
	"regexp"
)

// htmlTagRE matches a single HTML tag, including multi-line tags. (?s) makes
// `.`-class behavior span newlines via the negated class already, but we keep
// it explicit for clarity.
var htmlTagRE = regexp.MustCompile(`(?s)<[^>]*>`)

// stripHTML removes HTML tags and decodes HTML entities, mirroring the
// html_strip char filter that preceded tokenization in the old ES analyzer.
// Each tag is replaced with a space so adjacent words are not merged.
func stripHTML(s string) string {
	return html.UnescapeString(htmlTagRE.ReplaceAllString(s, " "))
}
