package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validHexKey is a 32-byte (64 hex char) blind key — the minimum LoadHasher
// accepts.
const validHexKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestValidateEncConfig(t *testing.T) {
	tests := []struct {
		name      string
		enc       EncConfig
		blind     BlindConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name: "valid disabled default arm C",
			enc:  EncConfig{Enabled: false, DefaultArm: "C"},
		},
		{
			name:  "valid enabled arm C",
			enc:   EncConfig{Enabled: true, DefaultArm: "C", MsgIndexPrefix: "enc-messages-v1"},
			blind: BlindConfig{Key: validHexKey, Version: "v1"},
		},
		{
			name:  "valid enabled arm A",
			enc:   EncConfig{Enabled: true, DefaultArm: "A", MsgIndexPrefix: "enc-messages-v1"},
			blind: BlindConfig{Key: validHexKey, Version: "v1"},
		},
		{
			name:  "valid enabled arm B",
			enc:   EncConfig{Enabled: true, DefaultArm: "B", MsgIndexPrefix: "enc-messages-v1"},
			blind: BlindConfig{Key: validHexKey, Version: "v1"},
		},
		{
			name:      "default arm A while disabled",
			enc:       EncConfig{Enabled: false, DefaultArm: "A"},
			wantErr:   true,
			errSubstr: "SEARCH_ENC_ENABLED",
		},
		{
			name:      "default arm B while disabled",
			enc:       EncConfig{Enabled: false, DefaultArm: "B"},
			wantErr:   true,
			errSubstr: "SEARCH_ENC_ENABLED",
		},
		{
			name:      "invalid arm string",
			enc:       EncConfig{Enabled: true, DefaultArm: "Z", MsgIndexPrefix: "enc-messages-v1"},
			blind:     BlindConfig{Key: validHexKey, Version: "v1"},
			wantErr:   true,
			errSubstr: "SEARCH_ENC_DEFAULT_ARM",
		},
		{
			name:      "empty index prefix when enabled",
			enc:       EncConfig{Enabled: true, DefaultArm: "C", MsgIndexPrefix: ""},
			blind:     BlindConfig{Key: validHexKey, Version: "v1"},
			wantErr:   true,
			errSubstr: "SEARCH_ENC_MSG_INDEX_PREFIX",
		},
		{
			name:      "bad index prefix (no version suffix)",
			enc:       EncConfig{Enabled: true, DefaultArm: "C", MsgIndexPrefix: "enc-messages"},
			blind:     BlindConfig{Key: validHexKey, Version: "v1"},
			wantErr:   true,
			errSubstr: "SEARCH_ENC_MSG_INDEX_PREFIX",
		},
		{
			name:      "empty blind key when enabled",
			enc:       EncConfig{Enabled: true, DefaultArm: "C", MsgIndexPrefix: "enc-messages-v1"},
			blind:     BlindConfig{Key: "", Version: "v1"},
			wantErr:   true,
			errSubstr: "blind",
		},
		{
			name:      "short blind key when enabled",
			enc:       EncConfig{Enabled: true, DefaultArm: "C", MsgIndexPrefix: "enc-messages-v1"},
			blind:     BlindConfig{Key: "00010203", Version: "v1"},
			wantErr:   true,
			errSubstr: "blind",
		},
		{
			name:      "non-hex blind key when enabled",
			enc:       EncConfig{Enabled: true, DefaultArm: "C", MsgIndexPrefix: "enc-messages-v1"},
			blind:     BlindConfig{Key: strings.Repeat("zz", 32), Version: "v1"},
			wantErr:   true,
			errSubstr: "blind",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEncConfig(tc.enc, tc.blind)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errSubstr != "" {
					assert.Contains(t, err.Error(), tc.errSubstr)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}
