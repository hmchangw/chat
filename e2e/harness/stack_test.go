//go:build e2e

package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComposeFilePath_ResolvesToRealFile(t *testing.T) {
	path, err := composeFilePath()
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(path, filepath.Join("docker-local", "e2e", "compose.e2e.yaml")), path)
	_, statErr := os.Stat(path)
	require.NoError(t, statErr)
}

func TestComposeFilePath_HonorsEnvOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-compose.yaml")
	require.NoError(t, os.WriteFile(override, []byte("name: stub\n"), 0o600))
	t.Setenv("E2E_COMPOSE_FILE", override)

	path, err := composeFilePath()
	require.NoError(t, err)
	assert.Equal(t, override, path)
}

func TestSiteEndpoints_DistinctPorts(t *testing.T) {
	a := siteAEndpoints("/tmp")
	b := siteBEndpoints("/tmp")
	assert.NotEqual(t, a.NATSURL, b.NATSURL)
	assert.NotEqual(t, a.MongoURI, b.MongoURI)
	assert.NotEqual(t, a.CassandraHosts[0], b.CassandraHosts[0])
	assert.NotEqual(t, a.ESURL, b.ESURL)
	assert.NotEqual(t, a.ValkeyAddr, b.ValkeyAddr)
	assert.NotEqual(t, a.KeycloakURL, b.KeycloakURL)
	assert.NotEqual(t, a.AuthURL, b.AuthURL)
	assert.Equal(t, "siteA", a.SiteID)
	assert.Equal(t, "siteB", b.SiteID)
}

// Port allocation sanity check: confirms the amended ports landed correctly
// (R2.A site-A +10000 band, site-B +20000 band; R4.B Mongo on 27117/27217
// to stay below the Linux ephemeral range starting at 32768).
func TestSiteEndpoints_AmendedPortsLanded(t *testing.T) {
	a := siteAEndpoints("/tmp")
	b := siteBEndpoints("/tmp")

	for _, tc := range []struct {
		name string
		url  string
	}{
		{"siteA NATS", a.NATSURL},
		{"siteA Mongo", a.MongoURI},
		{"siteA ES", a.ESURL},
		{"siteA auth", a.AuthURL},
		{"siteB NATS", b.NATSURL},
		{"siteB Mongo", b.MongoURI},
		{"siteB ES", b.ESURL},
		{"siteB auth", b.AuthURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotContains(t, tc.url, ":4222\"", tc.name+": must not use stock NATS port")
			assert.NotContains(t, tc.url, ":27017\"", tc.name+": must not use stock Mongo port")
			assert.NotContains(t, tc.url, ":9200\"", tc.name+": must not use stock ES port")
		})
	}

	// Specific amended ports per R2.A + R4.B.
	assert.Contains(t, a.NATSURL, ":14222")
	assert.Contains(t, a.MongoURI, ":27117")
	assert.Contains(t, b.NATSURL, ":24222")
	assert.Contains(t, b.MongoURI, ":27217")
}

func TestSiteEndpoints_KeycloakUsesHostDockerInternal(t *testing.T) {
	a := siteAEndpoints("/tmp")
	b := siteBEndpoints("/tmp")
	// Per amendment R1 3.D + R2.A: the harness reaches Keycloak via
	// host.docker.internal so the JWT iss claim matches what
	// auth-service-{a,b} verifies.
	assert.Contains(t, a.KeycloakURL, "host.docker.internal")
	assert.Contains(t, b.KeycloakURL, "host.docker.internal")
}
