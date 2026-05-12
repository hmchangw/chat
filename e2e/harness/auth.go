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

// Authenticate walks the Keycloak -> auth-service -> NATS chain for the given
// realm user. The realm uses fixed test users (alice, bob) preprovisioned in
// auth-service/deploy/keycloak/realm-export.json with password=password.
//
// Per amendment R1 8.A: the /auth request body carries only {ssoToken,
// natsPublicKey}. auth-service derives the `account` field from the JWT's
// preferred_username claim; passing an explicit account here is silently
// ignored at best and a spec violation at worst, so we omit it.
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

	// Seed the user record into the site's mongo `users` collection so
	// room-service / room-worker lookups succeed. Production has a separate
	// user-replication mechanism out of scope here; the test environment
	// simulates it on each Authenticate call. Idempotent upsert.
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

// AuthenticateE is an error-returning variant of Authenticate suitable for
// load tests and other code paths that invoke authentication from
// non-test goroutines. testing.T.FailNow (used by require.NoError inside
// Authenticate) calls runtime.Goexit, not panic -- which (a) is undefined
// when called off the main test goroutine, and (b) cannot be recover()'d.
// Callers that legitimately want to count failures rather than fail-fast
// must use this variant.
//
// NOTE: the returned Identity's conn is closed via t.Cleanup, so callers
// must still keep t alive until the test finishes.
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

// MintEphemeralUser creates a fresh Keycloak realm user (and registers
// cleanup to delete it at test-end). The returned account name can be
// passed to Authenticate to obtain a NATS conn scoped to a per-test
// identity, enabling parallel federation tests that previously had to
// run serial because they shared the realm-fixed `alice` / `bob`.
//
// Implementation: gets an admin token via the master realm
// password-grant (admin/admin -- e2e Keycloak bootstrap admin), POSTs
// to /admin/realms/chatapp/users to create the user with password
// "password", returns the username. Caller usually feeds this directly
// into Authenticate. Cleanup deletes the user via the admin API even
// when the test fails.
//
// Username pattern: "u-{8-char-hex}". Short to fit Keycloak's username
// constraints + readable in logs.
//
// Concurrency: safe for parallel callers. Keycloak's user-create
// endpoint serializes server-side; per-test users have disjoint
// usernames so no collisions.
func (s SiteEndpoints) MintEphemeralUser(t *testing.T, ctx context.Context) string {
	t.Helper()
	kc := keycloak.NewClient(s.KeycloakURL, s.Realm())

	// Admin token from master realm. Keycloak's bootstrap admin is the
	// only credential we have in the e2e harness; production would
	// rotate to a service-account client.
	adminKC := keycloak.NewClient(s.KeycloakURL, "master")
	adminTok, err := adminKC.AccessToken(ctx, "admin-cli", "admin", "admin")
	require.NoError(t, err, "obtain Keycloak master-realm admin token")

	// Retry-once on 409 collision (per bug-finder review): with 16 hex
	// chars the keyspace is 2^64 so collision is astronomical, but a
	// stale user from a crashed prior run can still be there.
	var username string
	for attempt := 0; attempt < 2; attempt++ {
		username = ephemeralUsername()
		err := kc.CreateUser(ctx, adminTok, username, "password")
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "409") && attempt == 0 {
			continue // retry with a fresh username
		}
		t.Fatalf("create ephemeral user %s on %s: %v", username, s.SiteID, err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Best-effort: re-obtain admin token in case the test ran past
		// the original token's expiry.
		if tok, err := adminKC.AccessToken(cleanupCtx, "admin-cli", "admin", "admin"); err == nil {
			_ = kc.DeleteUser(cleanupCtx, tok, username)
		}
	})

	return username
}

// MintEphemeralUserAs creates a Keycloak user with an EXPLICIT username,
// rather than the random ephemeral name MintEphemeralUser generates.
// Useful when a cross-site test needs the same username present in BOTH
// sites' Keycloak instances (federation keys off the account string, so
// the username must match). Cleanup is registered to delete the user on
// test end. Idempotent: a 409 dup-name from a prior test's leftover is
// treated as success.
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

// Realm returns the well-known chat realm name. Centralised so callers
// don't sprinkle the literal "chatapp" string and a future realm rename
// is a single-edit change.
func (s SiteEndpoints) Realm() string { return "chatapp" }

// ephemeralUsername returns a short unique username. 16 hex chars gives
// 2^64 keyspace (bumped from 4 bytes / 2^32 per bug-finder review:
// birthday-paradox 1% collision around ~9k usernames accumulated across
// long-lived REUSE-stack runs, which is realistic).
func ephemeralUsername() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("u-%x", b)
}
