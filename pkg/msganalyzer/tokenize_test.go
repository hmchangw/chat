package msganalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenizePattern(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "spaces", in: "hello world", want: []string{"hello", "world"}},
		{name: "punctuation splits", in: "a,b;c!d?e", want: []string{"a", "b", "c", "d", "e"}},
		{name: "brackets and quotes split", in: `("foo")[bar]{baz}`, want: []string{"foo", "bar", "baz"}},
		{name: "underscore is preserved", in: "foo_bar baz", want: []string{"foo_bar", "baz"}},
		{name: "hyphen and dot preserved", in: "a-b.c", want: []string{"a-b.c"}},
		{name: "angle brackets split", in: "a<b>c", want: []string{"a", "b", "c"}},
		{name: "leading and trailing delimiters", in: "  hi  ", want: []string{"hi"}},
		{name: "empty input", in: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tokenizePattern(tt.in))
		})
	}
}
