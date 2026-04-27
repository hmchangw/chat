package mongoutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildClientOptions(t *testing.T) {
	const uri = "mongodb://localhost:27017"

	tests := []struct {
		name         string
		username     string
		password     string
		expectAuth   bool
		expectedUser string
		expectedPass string
	}{
		{
			name:       "no credentials connects without auth",
			username:   "",
			password:   "",
			expectAuth: false,
		},
		{
			name:       "empty username with password skips auth",
			username:   "",
			password:   "secret",
			expectAuth: false,
		},
		{
			name:       "username with empty password skips auth",
			username:   "user",
			password:   "",
			expectAuth: false,
		},
		{
			name:         "both credentials set populates Auth",
			username:     "user",
			password:     "secret",
			expectAuth:   true,
			expectedUser: "user",
			expectedPass: "secret",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := buildClientOptions(uri, tc.username, tc.password)
			require.NotNil(t, opts)

			if !tc.expectAuth {
				assert.Nil(t, opts.Auth)
				return
			}

			require.NotNil(t, opts.Auth)
			assert.Equal(t, tc.expectedUser, opts.Auth.Username)
			assert.Equal(t, tc.expectedPass, opts.Auth.Password)
		})
	}
}
