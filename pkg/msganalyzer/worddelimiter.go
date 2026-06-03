package msganalyzer

import (
	"strings"
	"unicode"
)

// isWordRune reports whether r is part of a word: a Unicode letter or digit.
// CJK characters are letters and so pass here; they are split into bigrams by
// a later stage, not this one.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// wordDelimiter mirrors word_delimiter_graph with split_on_case_change=false,
// split_on_numerics=false, preserve_original=true. It splits a token into
// maximal alphanumeric runs; if the token contained any delimiter, the original
// is preserved ahead of its parts. An all-delimiter token yields nothing.
func wordDelimiter(tok string) []string {
	var parts []string
	var b strings.Builder
	hasDelim := false
	flush := func() {
		if b.Len() > 0 {
			parts = append(parts, b.String())
			b.Reset()
		}
	}
	for _, r := range tok {
		if isWordRune(r) {
			b.WriteRune(r)
		} else {
			hasDelim = true
			flush()
		}
	}
	flush()

	if len(parts) == 0 {
		return nil
	}
	if !hasDelim {
		return parts
	}
	out := make([]string, 0, len(parts)+1)
	out = append(out, tok)
	return append(out, parts...)
}
