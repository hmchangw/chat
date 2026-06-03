package msganalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text untouched", in: "hello world", want: "hello world"},
		{name: "tags become spaces", in: "<b>bold</b>", want: " bold "},
		{name: "tag between words does not merge them", in: "a<br>b", want: "a b"},
		{name: "entity decoded", in: "a&amp;b", want: "a&b"},
		{name: "lone lt without close survives", in: "a < b", want: "a < b"},
		{name: "multiline tag stripped", in: "x<div\nclass='y'>z", want: "x z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripHTML(tt.in))
		})
	}
}
