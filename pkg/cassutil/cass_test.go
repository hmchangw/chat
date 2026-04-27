package cassutil

import (
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCluster(t *testing.T) {
	hosts := []string{"cass-a:9042", "cass-b:9042"}
	const keyspace = "chat_test"

	tests := []struct {
		name         string
		username     string
		password     string
		expectAuth   bool
		expectedUser string
		expectedPass string
	}{
		{
			name:       "no credentials leaves Authenticator nil",
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
			name:         "both credentials set populates PasswordAuthenticator",
			username:     "user",
			password:     "secret",
			expectAuth:   true,
			expectedUser: "user",
			expectedPass: "secret",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cluster := buildCluster(hosts, keyspace, tc.username, tc.password)
			require.NotNil(t, cluster)

			assert.Equal(t, hosts, cluster.Hosts)
			assert.Equal(t, keyspace, cluster.Keyspace)
			assert.Equal(t, gocql.LocalQuorum, cluster.Consistency)
			assert.Equal(t, 10*time.Second, cluster.Timeout)

			if !tc.expectAuth {
				assert.Nil(t, cluster.Authenticator)
				return
			}

			auth, ok := cluster.Authenticator.(gocql.PasswordAuthenticator)
			require.True(t, ok, "expected PasswordAuthenticator, got %T", cluster.Authenticator)
			assert.Equal(t, tc.expectedUser, auth.Username)
			assert.Equal(t, tc.expectedPass, auth.Password)
		})
	}
}
