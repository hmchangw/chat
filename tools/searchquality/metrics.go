// Package main provides the search-quality harness. This file holds the pure,
// dependency-free ranking-quality metrics (no Elasticsearch, no I/O) used to
// quantify how much search quality the encrypted (blind) index costs relative
// to the plaintext index. Every function is deterministic: slices in, float out.
package main

import "math"

// RecallAtK returns the fraction of distinct relevant IDs that appear within the
// first k entries of ranked. Both inputs are treated as sets (duplicates are
// ignored), so the result is always in [0, 1].
//
// Edge cases (documented decisions):
//   - empty/nil relevant -> 1.0: there is nothing to miss, so recall is perfect.
//   - k <= 0 -> 0.0: no results are inspected, so nothing can be recalled.
//   - k > len(ranked) -> all of ranked is used (k is clamped to len(ranked)).
func RecallAtK(relevant []string, ranked []string, k int) float64 {
	relevantSet := toSet(relevant)
	if len(relevantSet) == 0 {
		return 1.0
	}
	if k <= 0 {
		return 0.0
	}
	if k > len(ranked) {
		k = len(ranked)
	}

	seen := make(map[string]struct{}, k)
	hits := 0
	for _, id := range ranked[:k] {
		if _, isRelevant := relevantSet[id]; !isRelevant {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		hits++
	}
	return float64(hits) / float64(len(relevantSet))
}

// Jaccard returns the Jaccard similarity |a ∩ b| / |a ∪ b| treating each slice
// as a set (duplicates ignored, order irrelevant). The result is in [0, 1].
//
// Edge cases (documented decisions):
//   - both empty/nil -> 1.0: two empty sets are defined as identical.
//   - exactly one empty -> 0.0: the union is non-empty and the intersection is empty.
func Jaccard(a, b []string) float64 {
	setA := toSet(a)
	setB := toSet(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 1.0
	}

	intersection := 0
	for id := range setA {
		if _, ok := setB[id]; ok {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	return float64(intersection) / float64(union)
}

// RBO computes Rank-Biased Overlap between two ranked lists with persistence p
// (0 <= p < 1; higher p weights deeper ranks more). The result is in [0, 1];
// higher means the two rankings agree more, weighted toward the top.
//
// Formula — finite (a.k.a. "min") RBO, NOT the extrapolated RBO_ext:
//
//	          sum_{d=1}^{D} w_d * A(d)
//	RBO  =  ---------------------------
//	            sum_{d=1}^{D} w_d
//
// where D = max(len(a), len(b)) is the deepest rank, the standard RBO weight is
//
//	w_d = (1 - p) * p^(d-1)
//
// and the agreement at depth d is the size of the overlap between the top-d
// prefixes divided by the depth:
//
//	A(d) = |prefix_a(d) ∩ prefix_b(d)| / d
//
// Dividing by the sum of the applied weights (sum_{d=1}^{D} w_d = 1 - p^D)
// normalizes the finite series so that two identical lists score exactly 1.0
// (the un-normalized truncated sum would only reach 1 - p^D). This is the
// standard finite/min RBO used when extrapolation past the observed depth is
// undesirable. See Webber, Moffat & Zobel (2010), "A Similarity Measure for
// Indefinite Rankings", ACM TOIS.
//
// Edge cases (documented decisions):
//   - both empty/nil -> 1.0: two empty rankings are defined as identical.
//   - exactly one empty -> 0.0: there is no possible overlap at any depth.
func RBO(a, b []string, p float64) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	maxDepth := len(a)
	if len(b) > maxDepth {
		maxDepth = len(b)
	}

	// Grow the prefix-overlap incrementally: at each depth add the newly seen
	// element of each list, counting an overlap when an element is present in both.
	seenA := make(map[string]struct{}, len(a))
	seenB := make(map[string]struct{}, len(b))
	overlap := 0

	var weightedSum float64
	var weightTotal float64
	for d := 1; d <= maxDepth; d++ {
		if d <= len(a) {
			id := a[d-1]
			if _, ok := seenB[id]; ok {
				overlap++
			}
			seenA[id] = struct{}{}
		}
		if d <= len(b) {
			id := b[d-1]
			if _, ok := seenA[id]; ok {
				overlap++
			}
			seenB[id] = struct{}{}
		}

		agreement := float64(overlap) / float64(d)
		weight := (1 - p) * math.Pow(p, float64(d-1))
		weightedSum += weight * agreement
		weightTotal += weight
	}

	if weightTotal == 0 {
		// Only reachable if p < 0; treat as degenerate and report no agreement.
		return 0.0
	}
	return weightedSum / weightTotal
}

// toSet returns the distinct elements of ids as a set.
func toSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}
