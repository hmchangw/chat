package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnvOr(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		envSet   bool
		def      string
		want     string
	}{
		{name: "set returns value", envValue: "x", envSet: true, def: "fallback", want: "x"},
		{name: "unset returns default", envSet: false, def: "fallback", want: "fallback"},
		{name: "empty returns default", envValue: "", envSet: true, def: "fallback", want: "fallback"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const key = "FEDERATION_INIT_TEST_VAR"
			if tc.envSet {
				t.Setenv(key, tc.envValue)
			}
			got := envOr(key, tc.def)
			assert.Equal(t, tc.want, got)
		})
	}
}
