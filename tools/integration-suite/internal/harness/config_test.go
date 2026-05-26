package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_RequiresPrimarySite(t *testing.T) {
	t.Setenv("SITES", "tw")
	t.Setenv("PRIMARY_SITE", "") // intentionally unset
	t.Setenv("AUTH_SERVICE_URL_TW", "")
	t.Setenv("ROOM_SERVICE_URL_TW", "")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PRIMARY_SITE")
}

func TestLoadConfig_TwoSitesParsed(t *testing.T) {
	t.Setenv("SITES", "tw,us")
	t.Setenv("PRIMARY_SITE", "tw")
	t.Setenv("AUTH_SERVICE_URL_TW", "http://auth-tw:8080")
	t.Setenv("ROOM_SERVICE_URL_TW", "http://room-tw:8080")
	t.Setenv("NATS_URL_TW", "nats://nats-tw:4222")
	t.Setenv("AUTH_SERVICE_URL_US", "http://auth-us:8080")
	t.Setenv("ROOM_SERVICE_URL_US", "http://room-us:8080")
	t.Setenv("NATS_URL_US", "nats://nats-us:4222")

	cfg, err := LoadConfig()
	require.NoError(t, err)

	assert.Equal(t, []string{"tw", "us"}, cfg.Sites)
	assert.Equal(t, "tw", cfg.PrimarySite)
	assert.Equal(t, "http://auth-tw:8080", cfg.AuthServiceURL("tw"))
	assert.Equal(t, "http://room-us:8080", cfg.RoomServiceURL("us"))
	assert.Equal(t, "nats://nats-tw:4222", cfg.NATSURL("tw"))
	assert.Equal(t, "nats://nats-us:4222", cfg.NATSURL("us"))
}

func TestLoadConfig_MissingServiceURLForSite(t *testing.T) {
	t.Setenv("SITES", "tw")
	t.Setenv("PRIMARY_SITE", "tw")
	t.Setenv("AUTH_SERVICE_URL_TW", "") // intentionally unset
	t.Setenv("ROOM_SERVICE_URL_TW", "")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AUTH_SERVICE_URL_TW")
}

func TestLoadConfig_PrimarySiteNotInSitesList(t *testing.T) {
	t.Setenv("SITES", "tw")
	t.Setenv("PRIMARY_SITE", "us")
	t.Setenv("AUTH_SERVICE_URL_TW", "http://auth-tw:8080")
	t.Setenv("ROOM_SERVICE_URL_TW", "http://room-tw:8080")
	t.Setenv("NATS_URL_TW", "nats://nats-tw:4222")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PRIMARY_SITE")
	assert.Contains(t, err.Error(), "not in SITES")
}

func TestLoadConfig_MissingNATSURLForSite(t *testing.T) {
	t.Setenv("SITES", "tw")
	t.Setenv("PRIMARY_SITE", "tw")
	t.Setenv("AUTH_SERVICE_URL_TW", "http://auth-tw:8080")
	t.Setenv("ROOM_SERVICE_URL_TW", "http://room-tw:8080")
	// NATS_URL_TW intentionally unset

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NATS_URL_TW")
}
