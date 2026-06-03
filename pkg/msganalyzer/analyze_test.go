package msganalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "lowercases", in: "Hello WORLD", want: []string{"hello", "world"}},
		{
			name: "html stripped then tokenized",
			in:   "<p>Hello, World!</p>",
			want: []string{"hello", "world"},
		},
		{
			name: "underscore identifier keeps original and parts, lowercased",
			in:   "Foo_Bar",
			want: []string{"foo_bar", "foo", "bar"},
		},
		{
			name: "cjk bigrammed and lowercased ascii",
			in:   "中文字 ABC",
			want: []string{"中文", "文字", "abc"},
		},
		{
			name: "mixed html cjk and identifier",
			in:   "<b>用户_Name</b> 中文",
			want: []string{"用户_name", "用户", "name", "中文"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Analyze(tt.in))
		})
	}
}

// Self-consistency is the real invariant: the same text analyzed twice must
// produce identical token streams (index-time == query-time).
func TestAnalyze_Deterministic(t *testing.T) {
	in := "<i>Quarterly</i> revenue_报告 up 12%"
	assert.Equal(t, Analyze(in), Analyze(in))
}

func TestAppendLower(t *testing.T) {
	tests := []struct {
		name string
		dst  []string
		in   string
		want []string
	}{
		{name: "appends lowercased", dst: nil, in: "ABC", want: []string{"abc"}},
		{name: "appends to existing", dst: []string{"x"}, in: "Y", want: []string{"x", "y"}},
		{name: "skips empty", dst: []string{"x"}, in: "", want: []string{"x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, appendLower(tt.dst, tt.in))
		})
	}
}
