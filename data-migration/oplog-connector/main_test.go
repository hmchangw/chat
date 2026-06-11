package main

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

func TestConnector_Fatal(t *testing.T) {
	c := &connector{fatal: make(chan error, 1)}
	c.fatal <- errors.New("boom")
	require.Error(t, <-c.Fatal())
}

func TestReadPreference(t *testing.T) {
	tests := []struct {
		in   string
		want readpref.Mode
	}{
		{"primary", readpref.PrimaryMode},
		{"primaryPreferred", readpref.PrimaryPreferredMode},
		{"secondary", readpref.SecondaryMode},
		{"", readpref.SecondaryMode}, // default
		{"secondaryPreferred", readpref.SecondaryPreferredMode},
		{"NEAREST", readpref.NearestMode}, // case-insensitive
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			rp, err := readPreference(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, rp.Mode())
		})
	}
}

func TestReadPreference_Invalid(t *testing.T) {
	_, err := readPreference("quorum")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid READ_PREFERENCE")
}

func TestParseLevel(t *testing.T) {
	assert.Equal(t, slog.LevelDebug, parseLevel("debug"))
	assert.Equal(t, slog.LevelWarn, parseLevel("WARN"))
	assert.Equal(t, slog.LevelError, parseLevel("error"))
	assert.Equal(t, slog.LevelInfo, parseLevel("info"))
	assert.Equal(t, slog.LevelInfo, parseLevel("bogus")) // default
}

func TestToSet(t *testing.T) {
	got := toSet([]string{"rocketchat_message", " users ", ""})
	assert.True(t, got["rocketchat_message"])
	assert.True(t, got["users"], "entries are trimmed")
	assert.True(t, got[""])
	assert.False(t, got["absent"])
}
