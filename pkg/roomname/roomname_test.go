package roomname

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTruncateRunes(t *testing.T) {
	tests := map[string]struct {
		in   string
		max  int
		want string
	}{
		"shorter than max":     {"hello", 100, "hello"},
		"truncated ascii":      {"hello", 3, "hel"},
		"truncated multibyte":  {"你好世界", 2, "你好"},
		"exact length":         {"你好世界", 4, "你好世界"},
		"longer than max":      {"你好世界", 100, "你好世界"},
		"empty":                {"", 5, ""},
		"mixed ascii/chinese":  {"hello世界", 6, "hello世"},
		"single rune at limit": {"a", 1, "a"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := TruncateRunes(tc.in, tc.max)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, utf8.RuneCountInString(got), tc.max)
		})
	}
}

func TestStripAccount(t *testing.T) {
	tests := map[string]struct {
		in      []string
		account string
		want    []string
	}{
		"present":     {[]string{"alice", "bob"}, "bob", []string{"alice"}},
		"absent":      {[]string{"alice", "carol"}, "bob", []string{"alice", "carol"}},
		"all matches": {[]string{"alice", "alice"}, "alice", []string{}},
		"empty input": {[]string{}, "alice", []string{}},
		"order kept":  {[]string{"a", "b", "c", "b"}, "b", []string{"a", "c"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := StripAccount(tc.in, tc.account)
			assert.Equal(t, tc.want, got)
		})
	}
}
