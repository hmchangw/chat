package msganalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCJKBigram(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "ascii passes through whole", in: "hello", want: []string{"hello"}},
		{name: "two han chars to one bigram", in: "中文", want: []string{"中文"}},
		{name: "three han chars to overlapping bigrams", in: "中文字", want: []string{"中文", "文字"}},
		{name: "lone cjk char is a unigram", in: "中", want: []string{"中"}},
		{name: "cjk then ascii", in: "中文abc", want: []string{"中文", "abc"}},
		{name: "ascii surrounding single cjk", in: "a中b", want: []string{"a", "中", "b"}},
		{name: "empty", in: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cjkBigram(tt.in))
		})
	}
}
