// Package emoji validates reaction shortcodes against a static allowlist of
// Unicode-emoji shortcodes and a site-scoped custom emoji store.
//
// Shortcodes are bare (no wrapping colons). Format:
//
//	first char  : [a-z0-9]
//	body chars  : [a-z0-9_+-]
//	total length: 1..32
//
// Unicode shortcodes ("thumbsup", "tada", "+1") are matched against the
// built-in allowlist. Custom shortcodes ("acme_party") are resolved via the
// caller-supplied CustomEmojiLookup, scoped by siteID.
package emoji

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// shortcodeRe enforces the wire format for a bare reaction shortcode.
var shortcodeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_+-]{0,31}$`)

// ErrInvalidShortcode is returned when the shortcode does not match the
// wire-format regex (case, length, character set, wrapping colons).
var ErrInvalidShortcode = errors.New("invalid reaction shortcode")

// ErrUnknownShortcode is returned when a syntactically valid shortcode is
// neither in the built-in Unicode allowlist nor in the site's custom emoji
// store.
var ErrUnknownShortcode = errors.New("unknown reaction shortcode")

// CustomEmojiLookup resolves a site-scoped custom emoji shortcode. Implementations
// typically query the custom_emojis Mongo collection.
type CustomEmojiLookup interface {
	CustomEmojiExists(ctx context.Context, siteID, shortcode string) (bool, error)
}

// Validator validates reaction shortcodes against the built-in allowlist and
// a site-scoped custom emoji store.
type Validator struct {
	lookup CustomEmojiLookup
}

// NewValidator returns a Validator backed by the given custom emoji lookup.
func NewValidator(lookup CustomEmojiLookup) *Validator {
	return &Validator{lookup: lookup}
}

// Validate reports whether shortcode is acceptable as a reaction on a message
// in the given site. It returns ErrInvalidShortcode for malformed input and
// ErrUnknownShortcode when the shortcode is well-formed but neither built-in
// nor a known custom emoji for the site.
func (v *Validator) Validate(ctx context.Context, siteID, shortcode string) error {
	// Built-in allowlist comes first so well-known shortcodes that fail the
	// custom-shortcode regex (e.g. "+1", "-1") still validate. Built-ins are
	// controlled code, so we trust the allowlist's contents.
	if IsBuiltinShortcode(shortcode) {
		return nil
	}
	if !shortcodeRe.MatchString(shortcode) {
		return fmt.Errorf("validate shortcode %q: %w", shortcode, ErrInvalidShortcode)
	}
	ok, err := v.lookup.CustomEmojiExists(ctx, siteID, shortcode)
	if err != nil {
		return fmt.Errorf("lookup custom emoji %q for site %q: %w", shortcode, siteID, err)
	}
	if !ok {
		return fmt.Errorf("validate shortcode %q: %w", shortcode, ErrUnknownShortcode)
	}
	return nil
}
