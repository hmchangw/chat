// Package msganalyzer is a Go port of the Elasticsearch message-content
// analyzer (html_strip → pattern tokenizer → word_delimiter_graph →
// cjk_bigram → lowercase). It exists so message content can be tokenized
// outside Elasticsearch, allowing the resulting tokens to be blind-hashed
// (see pkg/blindidx) before indexing. The SAME Analyze function must be used
// at index time and query time so hashed terms line up.
//
// Two intentional divergences from the legacy ES custom_analyzer were measured
// in Phase 2 (see spec §15):
//
//  1. CJK: Analyze bigrams CJK runs (e.g. "公园散步" → "公园","园散","散步"),
//     whereas the ES analyzer keeps whole CJK runs — its cjk_bigram filter is
//     inert because the pattern tokenizer emits word-typed (not CJK-typed)
//     tokens. Our bigramming is intentionally better for CJK substring search.
//  2. Ampersand: ES html_strip decodes/keeps a bare "&" as a token, whereas
//     Analyze drops it. Benign.
package msganalyzer

import "strings"

// Analyze runs the full pipeline and returns the ordered token stream.
//
// CJK bigramming applies only to delimiter-free tokens. The preserved-original
// token emitted by wordDelimiter still contains its delimiters (e.g.
// "用户_Name"); it is emitted whole (lowercased) rather than bigrammed, because
// bigramming a delimited token would split it at the CJK/ASCII boundary and
// leak a delimiter-attached fragment like "_name". Delimiter-free tokens —
// the split parts, and any token that never had a delimiter (e.g. "中文字") —
// are bigrammed normally.
func Analyze(text string) []string {
	stripped := stripHTML(text)
	var out []string
	for _, tok := range tokenizePattern(stripped) {
		for _, wd := range wordDelimiter(tok) {
			if hasDelimiter(wd) {
				out = appendLower(out, wd)
				continue
			}
			for _, cj := range cjkBigram(wd) {
				out = appendLower(out, cj)
			}
		}
	}
	return out
}

// appendLower lowercases s and appends it to dst unless it is empty.
func appendLower(dst []string, s string) []string {
	if low := strings.ToLower(s); low != "" {
		return append(dst, low)
	}
	return dst
}

// hasDelimiter reports whether s contains any non-word rune — i.e. a character
// wordDelimiter would split on. Such a token is a preserved original and is
// emitted whole rather than CJK-bigrammed.
func hasDelimiter(s string) bool {
	for _, r := range s {
		if !isWordRune(r) {
			return true
		}
	}
	return false
}
