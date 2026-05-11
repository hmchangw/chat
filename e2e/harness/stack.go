//go:build e2e

package harness

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	tcompose "github.com/testcontainers/testcontainers-go/modules/compose"
)

// Stack holds the running e2e topology: the compose project plus per-site
// endpoint URLs the test code can use to talk to NATS, Mongo, Cassandra,
// Elasticsearch, Valkey, Keycloak, and auth-service over the host network.
//
// Port mappings follow amendment R2.A (with R4.B's Mongo revision): site-A
// on the +10000 host band, site-B on +20000.
type Stack struct {
	compose tcompose.ComposeStack
	reused  bool

	SiteA SiteEndpoints
	SiteB SiteEndpoints
}

// SiteEndpoints holds host-network URLs for one site's exposed deps + auth.
// All values are populated regardless of E2E_REUSE_STACK because they're
// derived from the docker-compose port mappings, not from runtime inspection.
type SiteEndpoints struct {
	SiteID         string
	NATSURL        string
	NATSCredsFile  string // host path to backend.creds
	MongoURI       string
	CassandraHosts []string
	ESURL          string
	ValkeyAddr     string
	KeycloakURL    string
	AuthURL        string
}

// Start brings up the e2e topology and waits for healthchecks, OR (when
// E2E_REUSE_STACK=1) attaches to an already-running stack. Caller is
// responsible for calling Stop.
func Start(ctx context.Context) (*Stack, error) {
	if err := preflight(); err != nil {
		return nil, err
	}

	composePath, err := composeFilePath()
	if err != nil {
		return nil, fmt.Errorf("locate compose file: %w", err)
	}
	composeDir := filepath.Dir(composePath)

	stack := &Stack{
		SiteA: siteAEndpoints(composeDir),
		SiteB: siteBEndpoints(composeDir),
	}

	if reuse, _ := strconv.ParseBool(os.Getenv("E2E_REUSE_STACK")); reuse {
		stack.reused = true
		// Caller assumes the stack is running; harness verifies endpoints
		// during client construction (chapter 8).
		return stack, nil
	}

	cs, err := tcompose.NewDockerCompose(composePath)
	if err != nil {
		return nil, fmt.Errorf("create compose stack: %w", err)
	}
	stack.compose = cs

	if err := cs.Up(ctx, tcompose.Wait(true)); err != nil {
		// Compose-up failed mid-bring-up; drop everything so the next attempt
		// starts from a known-clean state. RemoveVolumes is intentional here
		// (not the day-to-day `make e2e-down` behavior) -- a half-built stack
		// is more likely than not to have partial DB state we don't want to
		// inherit.
		_ = cs.Down(ctx,
			tcompose.RemoveOrphans(true),
			tcompose.RemoveImagesLocal,
			tcompose.RemoveVolumes(true),
		)
		return nil, fmt.Errorf("compose up: %w", err)
	}

	return stack, nil
}

// Stop tears the stack down. No-op when the stack was reused (we never
// owned the lifecycle). When we did own it, we drop volumes too so the
// next run starts from a known-clean state -- consistent with the
// `make e2e-down-clean` semantics in the Makefile.
func (s *Stack) Stop(ctx context.Context) error {
	if s.reused || s.compose == nil {
		return nil
	}
	return s.compose.Down(ctx,
		tcompose.RemoveOrphans(true),
		tcompose.RemoveVolumes(true),
	)
}

// composeFilePath finds docker-local/e2e/compose.e2e.yaml by walking up from
// the test process's working directory until it finds a go.mod, then resolving
// the compose path relative to that. Avoids depending on runtime.Caller (which
// returns the file path captured at compile time and breaks under `-trimpath`
// or when the binary moves between machines). Override via E2E_COMPOSE_FILE.
//
// Per amendment R1 H10.
func composeFilePath() (string, error) {
	if env := os.Getenv("E2E_COMPOSE_FILE"); env != "" {
		return env, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			path := filepath.Join(dir, "docker-local", "e2e", "compose.e2e.yaml")
			if _, err := os.Stat(path); err != nil {
				return "", fmt.Errorf("compose file %s: %w", path, err)
			}
			return path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find go.mod walking up from cwd; set E2E_COMPOSE_FILE")
		}
		dir = parent
	}
}

// preflight fails fast on environment issues the harness can detect before
// touching Docker. The host.docker.internal check forward-looking flag from
// the chapter-4 review: the e2e harness reaches Keycloak via that hostname
// (matches auth-service-{a,b}'s OIDC_ISSUER_URL); a name-resolution failure
// produces a clearer error than a "connection refused" mid-test.
func preflight() error {
	if _, err := net.LookupHost("host.docker.internal"); err != nil {
		return fmt.Errorf(
			"host.docker.internal does not resolve on this host: %w\n"+
				"  On Docker Desktop (Mac/Windows) it's automatic. On plain Linux\n"+
				"  Docker Engine, run once:\n"+
				"    echo '127.0.0.1 host.docker.internal' | sudo tee -a /etc/hosts",
			err,
		)
	}
	return nil
}

// siteAEndpoints / siteBEndpoints return the host-network URL set the test
// code uses to talk to each site. Hard-coded against the host port mappings
// declared in compose.e2e.yaml (chapters 3 + 5; revised by amendments R2.A
// and R4.B). composeDir is the absolute path to docker-local/e2e/, used to
// compute the host path to backend.creds.
//
// Site-A host band: +10000 (NATS 14222, Mongo 27117, Cassandra 19042, ES
// 19200, Valkey 16379, Keycloak 18180, auth-service 18080). Keycloak uses
// host.docker.internal:18180 (not localhost:18180) so the JWT iss claim
// minted by Keycloak matches what auth-service-a verifies in-network.
func siteAEndpoints(composeDir string) SiteEndpoints {
	return SiteEndpoints{
		SiteID:         "siteA",
		NATSURL:        "nats://localhost:14222",
		NATSCredsFile:  filepath.Join(composeDir, "secrets", "backend.creds"),
		MongoURI:       "mongodb://localhost:27117",
		CassandraHosts: []string{"localhost:19042"},
		ESURL:          "http://localhost:19200",
		ValkeyAddr:     "localhost:16379",
		KeycloakURL:    "http://host.docker.internal:18180",
		AuthURL:        "http://localhost:18080",
	}
}

// Site-B host band: +20000.
func siteBEndpoints(composeDir string) SiteEndpoints {
	return SiteEndpoints{
		SiteID:         "siteB",
		NATSURL:        "nats://localhost:24222",
		NATSCredsFile:  filepath.Join(composeDir, "secrets", "backend.creds"),
		MongoURI:       "mongodb://localhost:27217",
		CassandraHosts: []string{"localhost:29042"},
		ESURL:          "http://localhost:29200",
		ValkeyAddr:     "localhost:26379",
		KeycloakURL:    "http://host.docker.internal:28180",
		AuthURL:        "http://localhost:28080",
	}
}
