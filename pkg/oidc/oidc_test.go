package oidc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContainsAudience(t *testing.T) {
	cases := []struct {
		name      string
		tokenAud  []string
		allowed   []string
		wantMatch bool
	}{
		{"single token aud matches single allowed", []string{"a"}, []string{"a"}, true},
		{"token aud matches one of many allowed", []string{"b"}, []string{"a", "b", "c"}, true},
		{"one of many token auds matches allowed", []string{"x", "b"}, []string{"a", "b"}, true},
		{"no match", []string{"x"}, []string{"a", "b"}, false},
		{"empty token aud", nil, []string{"a"}, false},
		{"empty allowed", []string{"a"}, nil, false},
		{"both empty", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantMatch, containsAudience(tc.tokenAud, tc.allowed))
		})
	}
}

func TestNewValidator_RejectsEmptyAudiences(t *testing.T) {
	_, err := NewValidator(t.Context(), Config{
		IssuerURL: "http://example.invalid",
		Audiences: nil,
	})
	assert.ErrorIs(t, err, ErrNoAudiences)
}

func TestClaims_Account(t *testing.T) {
	cases := []struct {
		name, preferred, fallback, want string
	}{
		{"preferred_username wins", "alice", "Alice Wang", "alice"},
		{"falls back to name", "", "Alice Wang", "Alice Wang"},
		{"both empty yields empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Claims{PreferredUsername: tc.preferred, Name: tc.fallback}
			assert.Equal(t, tc.want, c.Account())
		})
	}
}
