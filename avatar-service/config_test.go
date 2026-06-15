package main

import (
	"testing"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClusterBaseURL(t *testing.T) {
	c := config{ClusterDomains: clusterDomains{entries: []clusterDomain{{SiteID: "site-b", Domain: "https://avatar-b"}}}}
	assert.Equal(t, "https://avatar-b", c.clusterBaseURL("site-b"))
	assert.Equal(t, "", c.clusterBaseURL("unknown"))
}

func TestClusterDomains_UnmarshalText(t *testing.T) {
	var cd clusterDomains
	require.NoError(t, cd.UnmarshalText([]byte(`[{"siteID":"s1","domain":"https://a"},{"siteID":"s2","domain":"https://b"}]`)))
	assert.Equal(t, "https://a", cd.baseURL("s1"))
	assert.Equal(t, "https://b", cd.baseURL("s2"))
	assert.Equal(t, "", cd.baseURL("missing"))

	assert.Error(t, (&clusterDomains{}).UnmarshalText([]byte(`not json`)))
}

// TestClusterDomains_EnvParse proves caarlos0/env honors the TextUnmarshaler, so
// CLUSTER_DOMAINS is populated from a JSON env string.
func TestClusterDomains_EnvParse(t *testing.T) {
	t.Setenv("CD", `[{"siteID":"s1","domain":"https://a"}]`)
	type probe struct {
		CD clusterDomains `env:"CD"`
	}
	p, err := env.ParseAs[probe]()
	require.NoError(t, err)
	assert.Equal(t, "https://a", p.CD.baseURL("s1"))
}
