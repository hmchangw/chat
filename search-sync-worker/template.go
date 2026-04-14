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
// `es:"text,custom_analyzer"`. Untagged fields and fields tagged `es:"-"`
// are skipped.
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

		esType, analyzer, _ := strings.Cut(esTag, ",")
		prop := map[string]any{"type": esType}
		if analyzer != "" {
			prop["analyzer"] = analyzer
		}
		props[name] = prop
	}
	return props
}
