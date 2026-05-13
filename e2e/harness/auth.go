//go:build e2e

package harness

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/testutil/keycloak"
)

// Identity bundles everything a test needs to act as a specific user on a
// specific site: the realm-level account name, an authenticated NATS conn
// scoped to the user's JWT permissions, and the public NKey backing it.
type Identity struct {
	Account string
	NKey    nkeys.KeyPair
	NATSJWT string
	conn    *nats.Conn
}

// Conn returns the user-authenticated NATS connection.
func (id *Identity) Conn() *nats.Conn { return id.conn }

// Walks the Keycloak -> auth-service -> NATS chain. /auth derives `account`
// from the SSO token's preferred_username; no explicit field.
func (s SiteEndpoints) Authenticate(t *testing.T, ctx context.Context, account string) *Identity {
	t.Helper()

	kc := keycloak.NewClient(s.KeycloakURL, "chatapp")
	ssoToken, err := kc.AccessToken(ctx, "nats-chat", account, "password")
	require.NoError(t, err, "keycloak password grant for %s on %s", account, s.SiteID)

	kp, err := nkeys.CreateUser()
	require.NoError(t, err)
	pub, err := kp.PublicKey()
	require.NoError(t, err)

	authClient := s.HTTPClient(t)
	var resp struct {
		NATSJWT string `json:"natsJwt"`
	}
	httpResp, err := authClient.R().
		SetContext(ctx).
		SetBody(map[string]string{
			"ssoToken":      ssoToken,
			"natsPublicKey": pub,
		}).
		SetResult(&resp).
		Post("/auth")
	require.NoError(t, err, "POST /auth on %s", s.SiteID)
	require.False(t, httpResp.IsError(),
		"auth-service rejected: %d %s", httpResp.StatusCode(), httpResp.String())
	require.NotEmpty(t, resp.NATSJWT, "auth-service returned empty natsJwt")

	conn, err := nats.Connect(s.NATSURL,
		nats.UserJWTAndSeed(resp.NATSJWT, mustSeed(t, kp)),
	)
	require.NoError(t, err, "user-jwt nats connect on %s", s.SiteID)
	t.Cleanup(conn.Close)

	// Idempotent: seed the mongo user record so room-service lookups succeed.
	s.SeedRemoteUser(t, ctx, account, s.SiteID)

	return &Identity{
		Account: account,
		NKey:    kp,
		NATSJWT: resp.NATSJWT,
		conn:    conn,
	}
}

func mustSeed(t *testing.T, kp nkeys.KeyPair) string {
	t.Helper()
	seed, err := kp.Seed()
	require.NoError(t, err)
	return string(seed)
}

// AuthenticateE is Authenticate that returns an error instead of t.Fatal.
// Use from non-test goroutines (load tests); FailNow is undefined off-test.
func (s SiteEndpoints) AuthenticateE(t *testing.T, ctx context.Context, account string) (*Identity, error) {
	kc := keycloak.NewClient(s.KeycloakURL, "chatapp")
	ssoToken, err := kc.AccessToken(ctx, "nats-chat", account, "password")
	if err != nil {
		return nil, fmt.Errorf("keycloak password grant for %s on %s: %w", account, s.SiteID, err)
	}

	kp, err := nkeys.CreateUser()
	if err != nil {
		return nil, fmt.Errorf("create user nkey: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("nkey public key: %w", err)
	}

	authClient := s.HTTPClient(t)
	var resp struct {
		NATSJWT string `json:"natsJwt"`
	}
	httpResp, err := authClient.R().
		SetContext(ctx).
		SetBody(map[string]string{
			"ssoToken":      ssoToken,
			"natsPublicKey": pub,
		}).
		SetResult(&resp).
		Post("/auth")
	if err != nil {
		return nil, fmt.Errorf("POST /auth on %s: %w", s.SiteID, err)
	}
	if httpResp.IsError() {
		return nil, fmt.Errorf("auth-service rejected: %d %s", httpResp.StatusCode(), httpResp.String())
	}
	if resp.NATSJWT == "" {
		return nil, fmt.Errorf("auth-service returned empty natsJwt")
	}

	seed, err := kp.Seed()
	if err != nil {
		return nil, fmt.Errorf("nkey seed: %w", err)
	}
	conn, err := nats.Connect(s.NATSURL, nats.UserJWTAndSeed(resp.NATSJWT, string(seed)))
	if err != nil {
		return nil, fmt.Errorf("user-jwt nats connect on %s: %w", s.SiteID, err)
	}
	t.Cleanup(conn.Close)

	return &Identity{
		Account: account,
		NKey:    kp,
		NATSJWT: resp.NATSJWT,
		conn:    conn,
	}, nil
}

// MintEphemeralUser creates a per-test Keycloak user (password="password")
// and registers cleanup. Returns the random username "u-{16-hex}".
func (s SiteEndpoints) MintEphemeralUser(t *testing.T, ctx context.Context) string {
	t.Helper()
	kc := keycloak.NewClient(s.KeycloakURL, s.Realm())

	adminKC := keycloak.NewClient(s.KeycloakURL, "master")
	adminTok, err := adminKC.AccessToken(ctx, "admin-cli", "admin", "admin")
	require.NoError(t, err, "obtain Keycloak master-realm admin token")

	// Retry once on 409 in case a crashed prior run left the same name.
	var username string
	for attempt := 0; attempt < 2; attempt++ {
		username = ephemeralUsername()
		err := kc.CreateUser(ctx, adminTok, username, "password")
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "409") && attempt == 0 {
			continue
		}
		t.Fatalf("create ephemeral user %s on %s: %v", username, s.SiteID, err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if tok, err := adminKC.AccessToken(cleanupCtx, "admin-cli", "admin", "admin"); err == nil {
			_ = kc.DeleteUser(cleanupCtx, tok, username)
		}
	})

	return username
}

// MintEphemeralUserAs creates a Keycloak user with an explicit username.
// Use for cross-site tests that need the same name in both Keycloaks.
// Treats 409 (dup) as success so it's safe to call after a sibling site already created it.
func (s SiteEndpoints) MintEphemeralUserAs(t *testing.T, ctx context.Context, username string) {
	t.Helper()
	kc := keycloak.NewClient(s.KeycloakURL, s.Realm())
	adminKC := keycloak.NewClient(s.KeycloakURL, "master")
	adminTok, err := adminKC.AccessToken(ctx, "admin-cli", "admin", "admin")
	require.NoError(t, err, "obtain Keycloak master-realm admin token on %s", s.SiteID)

	if err := kc.CreateUser(ctx, adminTok, username, "password"); err != nil {
		// 409 is fine (a sibling test or earlier run already created it).
		if !strings.Contains(err.Error(), "409") {
			t.Fatalf("create user %s on %s: %v", username, s.SiteID, err)
		}
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if tok, err := adminKC.AccessToken(cleanupCtx, "admin-cli", "admin", "admin"); err == nil {
			_ = kc.DeleteUser(cleanupCtx, tok, username)
		}
	})
}

// Realm returns the chat realm name.
func (s SiteEndpoints) Realm() string { return "chatapp" }

// ephemeralUsername returns "u-{16-hex}" (2^64 keyspace).
func ephemeralUsername() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("u-%x", b)
}
