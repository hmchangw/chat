//go:build e2e

package scenarios

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/e2e"
)

// TestSmoke_AllDepsReachable runs first; if it fails the rest of the suite
// is meaningless. Asserts every per-site dep is responsive over the host
// network via the amended host-port band (R2.A / R4.B). Does NOT exercise
// per-user auth -- that's TestAuth_KeycloakRoundTrip in chapter 11.
func TestSmoke_AllDepsReachable(t *testing.T) {
	stack := e2e.Stack()
	require.NotNil(t, stack, "TestMain must have run harness.Start before scenarios")

	for _, site := range []*struct {
		name string
		ep   func() any
	}{
		{"siteA-nats", func() any { return stack.SiteA.SystemConn(t) }},
		{"siteA-mongo", func() any { return stack.SiteA.MongoDB(t) }},
		{"siteA-cass", func() any { return stack.SiteA.CassandraSession(t) }},
		{"siteB-nats", func() any { return stack.SiteB.SystemConn(t) }},
		{"siteB-mongo", func() any { return stack.SiteB.MongoDB(t) }},
		{"siteB-cass", func() any { return stack.SiteB.CassandraSession(t) }},
	} {
		t.Run(site.name, func(t *testing.T) {
			assert.NotNil(t, site.ep(), "client for %s must not be nil", site.name)
		})
	}

	t.Run("siteA-mongo-ping", func(t *testing.T) {
		db := stack.SiteA.MongoDB(t)
		err := db.RunCommand(t.Context(), bson.D{{Key: "ping", Value: 1}}).Err()
		require.NoError(t, err)
	})

	t.Run("siteB-mongo-ping", func(t *testing.T) {
		db := stack.SiteB.MongoDB(t)
		err := db.RunCommand(t.Context(), bson.D{{Key: "ping", Value: 1}}).Err()
		require.NoError(t, err)
	})

	t.Run("siteA-es-cluster-health", func(t *testing.T) {
		client, base := stack.SiteA.ESClient(t)
		resp, err := client.Get(base + "/_cluster/health")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("siteB-es-cluster-health", func(t *testing.T) {
		client, base := stack.SiteB.ESClient(t)
		resp, err := client.Get(base + "/_cluster/health")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("siteA-keycloak-realm", func(t *testing.T) {
		resp, err := http.Get(stack.SiteA.KeycloakURL + "/realms/chatapp")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("siteB-keycloak-realm", func(t *testing.T) {
		resp, err := http.Get(stack.SiteB.KeycloakURL + "/realms/chatapp")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}
