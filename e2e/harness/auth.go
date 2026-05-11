//go:build e2e

package harness

import (
	"context"
	"testing"

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
