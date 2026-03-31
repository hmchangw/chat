package main

import (
	"net/url"
	"strings"
)

// isBot returns true for system/bot accounts that should be excluded from membership.
func isBot(username string) bool {
	return strings.HasSuffix(username, ".bot") || strings.HasPrefix(username, "p_")
}

// extractDomain parses locationURL and returns the host portion.
func extractDomain(locationURL string) string {
	u, err := url.Parse(locationURL)
	if err != nil {
		return locationURL
	}
	return u.Host
}

// dedup returns a new slice with duplicates removed, preserving first-occurrence order.
func dedup(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// filterBots returns a new slice with bot usernames removed.
func filterBots(ss []string) []string {
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if !isBot(s) {
			result = append(result, s)
		}
	}
	return result
}
