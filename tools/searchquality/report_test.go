package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLangResult_GatePassed(t *testing.T) {
	tests := []struct {
		name                string
		recall, rbo         float64
		recallGate, rboGate float64
		want                bool
	}{
		{"both pass", 0.96, 0.91, 0.95, 0.90, true},
		{"recall fails", 0.90, 0.95, 0.95, 0.90, false},
		{"rbo fails", 0.99, 0.80, 0.95, 0.90, false},
		{"both at boundary pass", 0.95, 0.90, 0.95, 0.90, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := LangResult{Recall: tt.recall, RBO: tt.rbo}
			assert.Equal(t, tt.want, r.GatePassed(tt.recallGate, tt.rboGate))
		})
	}
}

func TestReport_OverallPassed(t *testing.T) {
	t.Run("all pass", func(t *testing.T) {
		rep := Report{RecallGate: 0.95, RBOGate: 0.90, Langs: []LangResult{
			{Lang: "english", Recall: 1.0, RBO: 1.0},
			{Lang: "cjk", Recall: 0.96, RBO: 0.92},
		}}
		assert.True(t, rep.OverallPassed())
	})
	t.Run("one fails", func(t *testing.T) {
		rep := Report{RecallGate: 0.95, RBOGate: 0.90, Langs: []LangResult{
			{Lang: "english", Recall: 1.0, RBO: 1.0},
			{Lang: "cjk", Recall: 0.50, RBO: 0.92},
		}}
		assert.False(t, rep.OverallPassed())
	})
	t.Run("empty fails", func(t *testing.T) {
		rep := Report{}
		assert.False(t, rep.OverallPassed())
	})
}

func TestRenderReport_ContainsTableAndVerdict(t *testing.T) {
	rep := Report{
		K: 10, P: 0.9, RecallGate: 0.95, RBOGate: 0.90,
		Langs: []LangResult{
			{Lang: "english", Queries: 4, Recall: 1.0, Jaccard: 1.0, RBO: 1.0, ParityDivergences: 0},
			{Lang: "cjk", Queries: 3, Recall: 0.50, Jaccard: 0.40, RBO: 0.60, ParityDivergences: 2,
				ParityExamples: []string{"cjk-3: go=[a] es=[b]"}},
		},
	}
	md := renderReport(rep)

	assert.Contains(t, md, "# Encrypted Search Quality Report")
	assert.Contains(t, md, "| Language | Queries | Recall@10 | Jaccard | RBO | Parity divergences | Gate |")
	assert.Contains(t, md, "| english | 4 | 1.000 | 1.000 | 1.000 | 0 | PASS |")
	assert.Contains(t, md, "| cjk | 3 | 0.500 | 0.400 | 0.600 | 2 | FAIL |")
	// cjk sorts before english.
	assert.Less(t, strings.Index(md, "| cjk |"), strings.Index(md, "| english |"))
	assert.Contains(t, md, "## Gate verdict: FAIL")
	assert.Contains(t, md, "cjk-3: go=[a] es=[b]")
}

func TestRenderReport_NoDivergences(t *testing.T) {
	rep := Report{
		K: 10, P: 0.9, RecallGate: 0.95, RBOGate: 0.90,
		Langs: []LangResult{{Lang: "english", Queries: 1, Recall: 1.0, Jaccard: 1.0, RBO: 1.0}},
	}
	md := renderReport(rep)
	assert.Contains(t, md, "## Gate verdict: PASS")
	assert.Contains(t, md, "No divergences detected")
}
