package msganalyzer

import "unicode"

// isCJK reports whether r is a CJK character handled by bigramming.
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// cjkBigram mirrors the cjk_bigram filter: maximal CJK runs become overlapping
// 2-grams (a lone CJK char stays a unigram), while non-CJK segments pass
// through unchanged and in order.
func cjkBigram(tok string) []string {
	runes := []rune(tok)
	var out []string
	for i := 0; i < len(runes); {
		if isCJK(runes[i]) {
			j := i
			for j < len(runes) && isCJK(runes[j]) {
				j++
			}
			run := runes[i:j]
			if len(run) == 1 {
				out = append(out, string(run))
			} else {
				for k := 0; k+1 < len(run); k++ {
					out = append(out, string(run[k:k+2]))
				}
			}
			i = j
			continue
		}
		j := i
		for j < len(runes) && !isCJK(runes[j]) {
			j++
		}
		out = append(out, string(runes[i:j]))
		i = j
	}
	return out
}
