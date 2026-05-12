package main

import (
	"testing"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfig_DefaultsAndOverrides exercises the env-tag round-trip for
// fields added in the post-R3 production change set (DURABLE_NAME and
// MAX_DELIVER), plus the existing ENCRYPTION_ENABLED gate so that an
// accidental envDefault drift on any of these surfaces here rather than
// at deploy time. Mirrors the inbox-worker bootstrap_test pattern.
func TestConfig_DefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name         string
		envs         map[string]string
		wantDurable  string
		wantMaxDeliv int
		wantEncrypt  bool
	}{
		{
			name:         "defaults",
			envs:         nil,
			wantDurable:  "broadcast-worker",
			wantMaxDeliv: 5, // poison-cliff guard per perf review (was -1)
			wantEncrypt:  false,
		},
		{
			name: "encryption variant - durable rename + bounded retries + encryption on",
			envs: map[string]string{
				"DURABLE_NAME":       "broadcast-worker-enc",
				"MAX_DELIVER":        "2",
				"ENCRYPTION_ENABLED": "true",
			},
			wantDurable:  "broadcast-worker-enc",
			wantMaxDeliv: 2,
			wantEncrypt:  true,
		},
		{
			name: "maxdeliver explicit -1 stays unlimited",
			envs: map[string]string{
				"MAX_DELIVER": "-1",
			},
			wantDurable:  "broadcast-worker",
			wantMaxDeliv: -1,
			wantEncrypt:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.envs {
				t.Setenv(k, v)
			}
			cfg, err := env.ParseAs[config]()
			require.NoError(t, err)
			assert.Equal(t, tc.wantDurable, cfg.DurableName,
				"DURABLE_NAME default + override round-trip")
			assert.Equal(t, tc.wantMaxDeliv, cfg.MaxDeliver,
				"MAX_DELIVER default + override round-trip")
			assert.Equal(t, tc.wantEncrypt, cfg.Encryption.Enabled,
				"ENCRYPTION_ENABLED default + override round-trip")
		})
	}
}

// TestConfig_MaxDeliverRejectsBadValue: env.ParseAs surfaces a parse
// error when MAX_DELIVER is non-integer. Catches: someone wires
// MAX_DELIVER="unlimited" expecting a string sentinel.
func TestConfig_MaxDeliverRejectsBadValue(t *testing.T) {
	t.Setenv("MAX_DELIVER", "unlimited")
	_, err := env.ParseAs[config]()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MaxDeliver",
		"parse error must name the offending field; got %s", err.Error())
}
