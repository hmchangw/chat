package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeSyncDMError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want string
	}{
		{"nil returns empty", nil, ""},
		{"missing request ID surfaced", errMissingRequestID, "missing X-Request-ID header"},
		{"invalid request ID surfaced", errInvalidRequestID, "invalid X-Request-ID header"},
		{"invalid sync DM request surfaced", errInvalidSyncDMRequest, "invalid sync DM request"},
		{"user lookup failed surfaced", errUserLookupFailed, "user lookup failed"},
		{"cross-site requester surfaced", errCrossSiteRequester, "requester is not on this site"},
		{"room ID collision surfaced", errRoomIDCollision, "room ID collision (existing room metadata mismatch)"},
		{"unknown error masked as internal", errors.New("mongo: connection refused"), "internal error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeSyncDMError(tc.in))
		})
	}
}
