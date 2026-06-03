package msganalyzer

import (
	"html"
	"regexp"
)

// htmlTagRE matches a single HTML tag. [^>] already spans newlines, so
// multi-line tags are handled without the (?s) flag.
var htmlTagRE = regexp.MustCompile(`<[^>]*>`)

// stripHTML removes HTML tags and decodes HTML entities, mirroring the
// html_strip char filter that preceded tokenization in the old ES analyzer.
// Each tag is replaced with a space so adjacent words are not merged.
func stripHTML(s string) string {
	return html.UnescapeString(htmlTagRE.ReplaceAllString(s, " "))
}
