package msganalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWordDelimiter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "no delimiter passes through", in: "hello", want: []string{"hello"}},
		{name: "underscore splits with original preserved first", in: "foo_bar", want: []string{"foo_bar", "foo", "bar"}},
		{name: "case change does NOT split", in: "camelCase", want: []string{"camelCase"}},
		{name: "letter-digit boundary does NOT split", in: "foo123", want: []string{"foo123"}},
		{name: "mixed delimiter and digits", in: "foo_bar123", want: []string{"foo_bar123", "foo", "bar123"}},
		{name: "hyphen splits", in: "a-b-c", want: []string{"a-b-c", "a", "b", "c"}},
		{name: "all-delimiter yields nothing", in: "---", want: nil},
		{name: "empty yields nothing", in: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, wordDelimiter(tt.in))
		})
	}
}
