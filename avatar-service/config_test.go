package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClusterBaseURL(t *testing.T) {
	c := config{ClusterDomains: map[string]string{"site-b": "https://avatar-b"}}
	assert.Equal(t, "https://avatar-b", c.clusterBaseURL("site-b"))
	assert.Equal(t, "", c.clusterBaseURL("unknown"))
}
