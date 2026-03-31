package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDedup(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "no duplicates preserves all elements in order",
			input:    []string{"alice", "bob", "charlie"},
			expected: []string{"alice", "bob", "charlie"},
		},
		{
			name:     "all duplicates returns single element",
			input:    []string{"alice", "alice", "alice"},
			expected: []string{"alice"},
		},
		{
			name:     "mixed duplicates keeps first occurrence",
			input:    []string{"alice", "bob", "alice", "charlie", "bob"},
			expected: []string{"alice", "bob", "charlie"},
		},
		{
			name:     "empty slice returns empty result",
			input:    []string{},
			expected: []string{},
		},
		{
			name:     "single element returns that element",
			input:    []string{"alice"},
			expected: []string{"alice"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := dedup(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFilterBots(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "all bots with .bot suffix returns empty",
			input:    []string{"system.bot", "admin.bot", "notifier.bot"},
			expected: []string{},
		},
		{
			name:     "all bots with p_ prefix returns empty",
			input:    []string{"p_system", "p_worker", "p_service"},
			expected: []string{},
		},
		{
			name:     "no bots returns all elements",
			input:    []string{"alice", "bob", "charlie"},
			expected: []string{"alice", "bob", "charlie"},
		},
		{
			name:     "mixed bots and non-bots returns only non-bots",
			input:    []string{"alice", "system.bot", "bob", "p_worker", "charlie"},
			expected: []string{"alice", "bob", "charlie"},
		},
		{
			name:     ".bot suffix match is filtered",
			input:    []string{"alice", "notifier.bot", "bob"},
			expected: []string{"alice", "bob"},
		},
		{
			name:     "p_ prefix match is filtered",
			input:    []string{"alice", "p_system", "bob"},
			expected: []string{"alice", "bob"},
		},
		{
			name:     "empty slice returns empty result",
			input:    []string{},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterBots(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
