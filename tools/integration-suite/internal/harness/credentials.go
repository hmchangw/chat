package harness

import (
	"fmt"

	"github.com/nats-io/nkeys"
)

// Credentials holds the per-user secrets the suite needs to interact
// with the system on behalf of an authenticated user.
type Credentials struct {
	Account string
	JWT     string
	Seed    string // nkey seed (encoded form, includes the "S" prefix)
}

// GenerateUserNkey creates a fresh NATS user nkey keypair.
// Returns (publicKey, seed, error). The public key is sent to
// auth-service; the seed is stored locally so the suite can sign
// NATS connection requests.
func GenerateUserNkey() (publicKey, seed string, err error) {
	kp, err := nkeys.CreateUser()
	if err != nil {
		return "", "", fmt.Errorf("credentials: create user nkey: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", "", fmt.Errorf("credentials: extract public key: %w", err)
	}
	s, err := kp.Seed()
	if err != nil {
		return "", "", fmt.Errorf("credentials: extract seed: %w", err)
	}
	return pub, string(s), nil
}
