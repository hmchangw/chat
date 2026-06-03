package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecallAtK(t *testing.T) {
	tests := []struct {
		name     string
		relevant []string
		ranked   []string
		k        int
		want     float64
	}{
		{
			name:     "full recall",
			relevant: []string{"a", "b"},
			ranked:   []string{"a", "b", "c"},
			k:        3,
			want:     1.0,
		},
		{
			name:     "half recall",
			relevant: []string{"a", "b"},
			ranked:   []string{"a", "x", "c"},
			k:        3,
			want:     0.5,
		},
		{
			name:     "zero recall",
			relevant: []string{"a", "b"},
			ranked:   []string{"x", "y", "z"},
			k:        3,
			want:     0.0,
		},
		{
			name:     "k truncates ranked so a relevant item below k is missed",
			relevant: []string{"a", "b"},
			ranked:   []string{"a", "x", "b"},
			k:        2,
			want:     0.5,
		},
		{
			name:     "k greater than len(ranked) uses all of ranked",
			relevant: []string{"a", "b"},
			ranked:   []string{"a", "b"},
			k:        10,
			want:     1.0,
		},
		{
			name:     "empty relevant is perfect by definition",
			relevant: []string{},
			ranked:   []string{"a", "b"},
			k:        2,
			want:     1.0,
		},
		{
			name:     "nil relevant is perfect by definition",
			relevant: nil,
			ranked:   []string{"a", "b"},
			k:        2,
			want:     1.0,
		},
		{
			name:     "k of zero yields zero",
			relevant: []string{"a"},
			ranked:   []string{"a", "b"},
			k:        0,
			want:     0.0,
		},
		{
			name:     "negative k yields zero",
			relevant: []string{"a"},
			ranked:   []string{"a", "b"},
			k:        -3,
			want:     0.0,
		},
		{
			name:     "empty ranked with non-empty relevant yields zero",
			relevant: []string{"a"},
			ranked:   []string{},
			k:        5,
			want:     0.0,
		},
		{
			name:     "duplicate relevant ids are treated as a set",
			relevant: []string{"a", "a", "b"},
			ranked:   []string{"a", "b", "c"},
			k:        3,
			want:     1.0,
		},
		{
			name:     "duplicate ranked ids do not inflate recall",
			relevant: []string{"a", "b"},
			ranked:   []string{"a", "a", "a"},
			k:        3,
			want:     0.5,
		},
		{
			name:     "k of one finds only the top item",
			relevant: []string{"a", "b"},
			ranked:   []string{"a", "b", "c"},
			k:        1,
			want:     0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RecallAtK(tt.relevant, tt.ranked, tt.k)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestJaccard(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want float64
	}{
		{
			name: "identical sets",
			a:    []string{"a", "b", "c"},
			b:    []string{"a", "b", "c"},
			want: 1.0,
		},
		{
			name: "disjoint sets",
			a:    []string{"a", "b"},
			b:    []string{"c", "d"},
			want: 0.0,
		},
		{
			name: "both empty is one by definition",
			a:    []string{},
			b:    []string{},
			want: 1.0,
		},
		{
			name: "both nil is one by definition",
			a:    nil,
			b:    nil,
			want: 1.0,
		},
		{
			name: "one empty one populated is zero",
			a:    []string{},
			b:    []string{"a"},
			want: 0.0,
		},
		{
			// {a,b,c} ∩ {b,c,d} = {b,c} (2); union = {a,b,c,d} (4) => 0.5
			name: "partial overlap two of four",
			a:    []string{"a", "b", "c"},
			b:    []string{"b", "c", "d"},
			want: 0.5,
		},
		{
			// {a,b} ∩ {a,b,c} = {a,b} (2); union = {a,b,c} (3) => 2/3
			name: "subset overlap",
			a:    []string{"a", "b"},
			b:    []string{"a", "b", "c"},
			want: 2.0 / 3.0,
		},
		{
			name: "duplicates within an input are deduped",
			a:    []string{"a", "a", "b"},
			b:    []string{"b", "b"},
			want: 0.5, // {a,b} ∩ {b} = {b} (1); union {a,b} (2)
		},
		{
			name: "order does not matter",
			a:    []string{"c", "a", "b"},
			b:    []string{"b", "c", "a"},
			want: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Jaccard(tt.a, tt.b)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestRBO(t *testing.T) {
	identical10 := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}

	t.Run("identical length-ten list is at least 0.99 with p of 0.9", func(t *testing.T) {
		got := RBO(identical10, identical10, 0.9)
		assert.GreaterOrEqual(t, got, 0.99)
		assert.InDelta(t, 1.0, got, 1e-9)
	})

	t.Run("disjoint lists are zero", func(t *testing.T) {
		got := RBO([]string{"a", "b"}, []string{"c", "d"}, 0.9)
		assert.InDelta(t, 0.0, got, 1e-9)
	})

	t.Run("both empty is one by definition", func(t *testing.T) {
		got := RBO([]string{}, []string{}, 0.9)
		assert.InDelta(t, 1.0, got, 1e-9)
	})

	t.Run("one empty one populated is zero", func(t *testing.T) {
		got := RBO([]string{}, []string{"a"}, 0.9)
		assert.InDelta(t, 0.0, got, 1e-9)
	})

	t.Run("reversed list ranks much lower than identical", func(t *testing.T) {
		got := RBO(identical10, []string{"9", "8", "7", "6", "5", "4", "3", "2", "1", "0"}, 0.9)
		assert.Less(t, got, 0.6)
	})

	// Hand-computed: a=[x,y], b=[x,z], p=0.9, maxdepth=2.
	// d=1: prefixes {x},{x} -> agreement = 1/1 = 1.
	// d=2: prefixes {x,y},{x,z} -> intersection {x} -> agreement = 1/2 = 0.5.
	// weights: w1 = (1-p) = 0.1, w2 = (1-p)*p = 0.09; sum = 0.19.
	// RBO = (1*0.1 + 0.5*0.09) / 0.19 = 0.145 / 0.19 = 0.76315789473...
	t.Run("share only the top element of length-two lists", func(t *testing.T) {
		got := RBO([]string{"x", "y"}, []string{"x", "z"}, 0.9)
		assert.InDelta(t, 0.145/0.19, got, 1e-9)
	})

	// Hand-computed: identical length-two list, p=0.5.
	// d=1: agreement 1, d=2: agreement 1.
	// weights: w1 = 0.5, w2 = 0.25; sum = 0.75.
	// RBO = (1*0.5 + 1*0.25)/0.75 = 1.0 (normalized identical -> 1).
	t.Run("identical length-two list normalizes to one", func(t *testing.T) {
		got := RBO([]string{"x", "y"}, []string{"x", "y"}, 0.5)
		assert.InDelta(t, 1.0, got, 1e-9)
	})

	// Hand-computed differing lengths: a=[x], b=[x,y], p=0.9, maxdepth=2.
	// d=1: {x} vs {x} -> 1. d=2: prefix of a is still {x} (len 1), b is {x,y};
	// intersection {x} = 1, divided by depth d=2 -> 0.5.
	// RBO = (1*0.1 + 0.5*0.09)/0.19 = 0.145/0.19.
	t.Run("differing lengths use depth as the denominator", func(t *testing.T) {
		got := RBO([]string{"x"}, []string{"x", "y"}, 0.9)
		assert.InDelta(t, 0.145/0.19, got, 1e-9)
	})

	t.Run("p of zero weights only the top rank", func(t *testing.T) {
		// With p=0, only d=1 carries weight; same top => 1.0, different top => 0.0.
		assert.InDelta(t, 1.0, RBO([]string{"a", "z"}, []string{"a", "q"}, 0.0), 1e-9)
		assert.InDelta(t, 0.0, RBO([]string{"a"}, []string{"b"}, 0.0), 1e-9)
	})

	t.Run("identical single-element lists are one", func(t *testing.T) {
		assert.InDelta(t, 1.0, RBO([]string{"a"}, []string{"a"}, 0.9), 1e-9)
	})
}
