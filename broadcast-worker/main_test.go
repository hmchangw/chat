package main

import (
	"testing"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Round-trips ENCRYPTION_ENABLED env so an envDefault drift surfaces
// here, not at deploy time.
func TestConfig_EncryptionDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name        string
		envs        map[string]string
		wantEncrypt bool
	}{
		{name: "default off", envs: nil, wantEncrypt: false},
		{name: "explicit on", envs: map[string]string{"ENCRYPTION_ENABLED": "true"}, wantEncrypt: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.envs {
				t.Setenv(k, v)
			}
			cfg, err := env.ParseAs[config]()
			require.NoError(t, err)
			assert.Equal(t, tc.wantEncrypt, cfg.Encryption.Enabled,
				"ENCRYPTION_ENABLED default + override round-trip")
		})
	}
}
