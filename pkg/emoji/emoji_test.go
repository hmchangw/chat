package emoji_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/emoji"
)

// stubLookup implements emoji.CustomEmojiLookup for tests.
type stubLookup struct {
	exists map[string]bool
	err    error
}

func (s *stubLookup) CustomEmojiExists(_ context.Context, _, shortcode string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.exists[shortcode], nil
}

func TestValidator_Validate_BuiltinShortcode(t *testing.T) {
	v := emoji.NewValidator(&stubLookup{})

	builtins := []string{"thumbsup", "thumbsdown", "+1", "-1", "tada", "heart", "fire", "eyes"}
	for _, sc := range builtins {
		sc := sc
		t.Run(sc, func(t *testing.T) {
			require.NoError(t, v.Validate(context.Background(), "site-a", sc))
		})
	}
}

func TestValidator_Validate_CustomShortcode_Found(t *testing.T) {
	lookup := &stubLookup{exists: map[string]bool{"acme_party": true}}
	v := emoji.NewValidator(lookup)

	require.NoError(t, v.Validate(context.Background(), "site-a", "acme_party"))
}

func TestValidator_Validate_CustomShortcode_NotFound(t *testing.T) {
	v := emoji.NewValidator(&stubLookup{exists: map[string]bool{}})

	err := v.Validate(context.Background(), "site-a", "not_a_real_emoji_xyz")
	require.Error(t, err)
	assert.ErrorIs(t, err, emoji.ErrUnknownShortcode)
}

func TestValidator_Validate_InvalidFormat(t *testing.T) {
	v := emoji.NewValidator(&stubLookup{})

	cases := []struct {
		name      string
		shortcode string
	}{
		{"empty", ""},
		{"wrapped in colons", ":thumbsup:"},
		{"leading colon", ":thumbsup"},
		{"trailing colon", "thumbsup:"},
		{"uppercase letters", "ThumbsUp"},
		{"space", "thumbs up"},
		{"unicode literal", "👍"},
		{"leading underscore", "_party"},
		{"leading plus", "+party"},
		{"leading dash", "-party"},
		{"trailing dot", "party."},
		{"slash", "party/time"},
		{"too long", strings.Repeat("a", 33)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(context.Background(), "site-a", tc.shortcode)
			require.Error(t, err)
			assert.ErrorIs(t, err, emoji.ErrInvalidShortcode)
		})
	}
}

func TestValidator_Validate_BoundaryLengths(t *testing.T) {
	lookup := &stubLookup{exists: map[string]bool{
		"a":                     true,
		strings.Repeat("a", 32): true,
	}}
	v := emoji.NewValidator(lookup)

	require.NoError(t, v.Validate(context.Background(), "site-a", "a"))
	require.NoError(t, v.Validate(context.Background(), "site-a", strings.Repeat("a", 32)))
}

func TestValidator_Validate_LookupError(t *testing.T) {
	sentinel := errors.New("mongo down")
	v := emoji.NewValidator(&stubLookup{err: sentinel})

	err := v.Validate(context.Background(), "site-a", "some_custom_emoji")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	// Built-in shortcodes must not hit the lookup at all.
	require.NoError(t, v.Validate(context.Background(), "site-a", "thumbsup"))
}

func TestValidator_Validate_PlusOneMinusOneAreBuiltin(t *testing.T) {
	// +1 and -1 are common shortcodes with no letters; they must validate
	// against the built-in allowlist without hitting the custom lookup.
	v := emoji.NewValidator(&stubLookup{err: errors.New("must not be called")})

	require.NoError(t, v.Validate(context.Background(), "site-a", "+1"))
	require.NoError(t, v.Validate(context.Background(), "site-a", "-1"))
}

func TestIsBuiltinShortcode(t *testing.T) {
	assert.True(t, emoji.IsBuiltinShortcode("thumbsup"))
	assert.True(t, emoji.IsBuiltinShortcode("+1"))
	assert.False(t, emoji.IsBuiltinShortcode("acme_party"))
	assert.False(t, emoji.IsBuiltinShortcode(":thumbsup:"))
}
