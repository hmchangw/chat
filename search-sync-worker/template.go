package main

import (
	"reflect"
	"strings"
)

// esPropertiesFromStruct reflects over struct T's fields to build an
// Elasticsearch mapping properties map from `es` struct tags. Fields are
// keyed by the `json` tag name.
//
// The `es` tag grammar is "type[,analyzer]" — e.g. `es:"keyword"` or
// `es:"text,custom_analyzer"`. Fields are skipped when:
//   - the `es` tag is missing or `-`
//   - the `json` tag is missing, empty, or `-` (fail closed: we never emit
//     a mapping entry under the empty string, which would silently corrupt
//     the template for any future struct that adds an `es`-tagged field
//     without a matching `json` tag)
func esPropertiesFromStruct[T any]() map[string]any {
	var zero T
	t := reflect.TypeOf(zero)
	props := make(map[string]any, t.NumField())
	for i := range t.NumField() {
		field := t.Field(i)
		esTag := field.Tag.Get("es")
		if esTag == "" || esTag == "-" {
			continue
		}
		jsonTag := field.Tag.Get("json")
		name, _, _ := strings.Cut(jsonTag, ",")
		if name == "" || name == "-" {
			continue
		}

		esType, analyzer, _ := strings.Cut(esTag, ",")
		prop := map[string]any{"type": esType}
		if analyzer != "" {
			prop["analyzer"] = analyzer
		}
		props[name] = prop
	}
	return props
}

// customAnalyzerSettings returns the `analysis` block shared by the
// spotlight (room-typeahead) and spotlight-org (section-typeahead)
// templates. A whitespace tokenizer with a permissive `token_chars`
// set (letter, digit, punctuation, symbol) feeds a lowercase-folding
// `custom_analyzer`. Returning a fresh map per call prevents aliasing
// if a caller mutates the result.
func customAnalyzerSettings() map[string]any {
	return map[string]any{
		"analyzer": map[string]any{
			"custom_analyzer": map[string]any{
				"type":      "custom",
				"tokenizer": "custom_tokenizer",
				"filter":    []string{"lowercase"},
			},
		},
		"tokenizer": map[string]any{
			"custom_tokenizer": map[string]any{
				"type":        "whitespace",
				"token_chars": []string{"letter", "digit", "punctuation", "symbol"},
			},
		},
	}
}

// indexTopology returns the (shards, replicas) pair an ES index
// template should declare. Prod values vary by collection — pass them
// in. In dev mode every template collapses to 1/0 so a single
// DEV_MODE toggle gives every index a fast local footprint without
// per-template env vars.
func indexTopology(prodShards, prodReplicas int, devMode bool) (int, int) {
	if devMode {
		return 1, 0
	}
	return prodShards, prodReplicas
}
