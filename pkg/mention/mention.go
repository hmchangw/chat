package mention

import (
	"regexp"
	"strings"
)

// mentionRe matches @mention tokens in message content.
// A bare @ not preceded by whitespace (e.g. "hello@bob") also matches —
// this is intentional per spec. Non-existent accounts are silently skipped
// during the user lookup.
var mentionRe = regexp.MustCompile(`(^|\s|>?)@([0-9a-zA-Z_-]+(\.[0-9a-zA-Z_-]+)*(@[0-9a-zA-Z_-]+(\.[0-9a-zA-Z_-]+)*)?)`)

// ParseResult holds parsed mention data extracted from message content.
type ParseResult struct {
	Accounts   []string // unique mentioned accounts, lowercased, excluding @all/@here
	MentionAll bool     // true if @all or @here was mentioned (case-insensitive)
}

// Parse extracts @mention tokens from content and returns the unique
// mentioned accounts along with whether @all or @here was present.
func Parse(content string) ParseResult {
	matches := mentionRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return ParseResult{}
	}

	var result ParseResult
	seen := make(map[string]struct{}, len(matches))

	for _, m := range matches {
		account := strings.ToLower(m[2])
		if account == "all" || account == "here" {
			result.MentionAll = true
			continue
		}
		if _, exists := seen[account]; !exists {
			seen[account] = struct{}{}
			result.Accounts = append(result.Accounts, account)
		}
	}

	return result
}
