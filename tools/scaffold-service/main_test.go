package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_RejectsInvalidNames(t *testing.T) {
	for _, n := range []string{"", "A", "service_name", "1service", "-leading-hyphen", "trailing-"} {
		err := run(n, t.TempDir(), false)
		assert.Error(t, err, "name %q must be rejected", n)
	}
}

func TestRun_EmitsExpectedLayout(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, run("presence-service", root, false))

	want := []string{
		"presence-service/main.go",
		"presence-service/handler.go",
		"presence-service/routes.go",
		"presence-service/store.go",
		"presence-service/handler_test.go",
		"presence-service/deploy/Dockerfile",
		"presence-service/deploy/docker-compose.yml",
		"presence-service/deploy/azure-pipelines.yml",
	}
	for _, p := range want {
		_, err := os.Stat(filepath.Join(root, p))
		assert.NoError(t, err, "expected %s to be written", p)
	}

	dockerfile, err := os.ReadFile(filepath.Join(root, "presence-service", "deploy", "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(dockerfile), "COPY presence-service/ presence-service/",
		"template substitution must replace .Name in Dockerfile")
}

func TestRun_RefusesOverwriteWithoutForce(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, run("presence-service", root, false))

	err := run("presence-service", root, false)
	assert.Error(t, err, "second run without -force must refuse")

	require.NoError(t, run("presence-service", root, true), "second run with -force must succeed")
}
