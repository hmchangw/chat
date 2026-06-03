package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildEncOptions_Disabled(t *testing.T) {
	opts, err := buildEncOptions(&config{EncEnabled: false})
	require.NoError(t, err)
	assert.False(t, opts.enabled)
	assert.Nil(t, opts.hasher, "disabled enc must not load a hasher")
	assert.Empty(t, opts.indexPrefix)
}

func TestBuildEncOptions_Enabled(t *testing.T) {
	cfg := config{
		EncEnabled:        true,
		EncMsgIndexPrefix: "enc-messages-site-a-v1",
		BlindKey:          strings.Repeat("ab", 32),
		BlindKeyVersion:   "v1",
	}
	opts, err := buildEncOptions(&cfg)
	require.NoError(t, err)
	assert.True(t, opts.enabled)
	assert.Equal(t, "enc-messages-site-a-v1", opts.indexPrefix)
	assert.Equal(t, "v1", opts.keyVersion)
	require.NotNil(t, opts.hasher)
	assert.Equal(t, "v1", opts.hasher.Version())
}

func TestBuildEncOptions_ValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  config
	}{
		{
			name: "missing index prefix",
			cfg: config{
				EncEnabled:      true,
				BlindKey:        strings.Repeat("ab", 32),
				BlindKeyVersion: "v1",
			},
		},
		{
			name: "unversioned index prefix",
			cfg: config{
				EncEnabled:        true,
				EncMsgIndexPrefix: "enc-messages",
				BlindKey:          strings.Repeat("ab", 32),
				BlindKeyVersion:   "v1",
			},
		},
		{
			name: "missing blind key",
			cfg: config{
				EncEnabled:        true,
				EncMsgIndexPrefix: "enc-messages-v1",
				BlindKeyVersion:   "v1",
			},
		},
		{
			name: "missing blind key version",
			cfg: config{
				EncEnabled:        true,
				EncMsgIndexPrefix: "enc-messages-v1",
				BlindKey:          strings.Repeat("ab", 32),
			},
		},
		{
			name: "non-hex blind key",
			cfg: config{
				EncEnabled:        true,
				EncMsgIndexPrefix: "enc-messages-v1",
				BlindKey:          "nothex!!",
				BlindKeyVersion:   "v1",
			},
		},
		{
			name: "enc and plaintext prefix share a base",
			cfg: config{
				EncEnabled:        true,
				MsgIndexPrefix:    "msgs-v1",
				EncMsgIndexPrefix: "msgs-v2",
				BlindKey:          strings.Repeat("ab", 32),
				BlindKeyVersion:   "v1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildEncOptions(&tt.cfg)
			assert.Error(t, err)
		})
	}
}
