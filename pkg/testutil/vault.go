//go:build integration

package testutil

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
	tcvault "github.com/testcontainers/testcontainers-go/modules/vault"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

// VaultHandle is the per-test view of the shared Vault container. The
// transit key it points at is provisioned freshly for each test, so
// rotation and other in-place mutations don't bleed between tests.
type VaultHandle struct {
	Address      string
	Token        string
	TransitMount string
	TransitKey   string

	client *vaultapi.Client
}

const (
	vaultRootToken    = "test-root-token"
	vaultTransitMount = "transit"
)

// vaultBase is the shared, process-wide Vault container handle: client,
// address, root token, and the (single) transit mount. Per-test keys
// are created on top of this.
type vaultBase struct {
	client  *vaultapi.Client
	address string
}

var (
	vaultOnce    sync.Once
	vaultBaseRef *vaultBase
	vaultInitErr error
)

func ensureVaultBase(ctx context.Context) (*vaultBase, error) {
	vaultOnce.Do(func() {
		container, err := tcvault.Run(
			ctx,
			testimages.Vault,
			tcvault.WithToken(vaultRootToken),
		)
		if err != nil {
			vaultInitErr = fmt.Errorf("start vault: %w", err)
			return
		}
		addr, err := container.HttpHostAddress(ctx)
		if err != nil {
			vaultInitErr = fmt.Errorf("get vault address: %w", err)
			return
		}
		cfg := vaultapi.DefaultConfig()
		cfg.Address = addr
		client, err := vaultapi.NewClient(cfg)
		if err != nil {
			vaultInitErr = fmt.Errorf("vault client: %w", err)
			return
		}
		client.SetToken(vaultRootToken)

		// Enable the transit engine once for the process. The transit
		// engine is a mount; the keys under it are namespaced per test.
		if err := client.Sys().MountWithContext(ctx, vaultTransitMount, &vaultapi.MountInput{
			Type: "transit",
		}); err != nil {
			vaultInitErr = fmt.Errorf("mount transit: %w", err)
			return
		}

		vaultBaseRef = &vaultBase{client: client, address: addr}
	})
	return vaultBaseRef, vaultInitErr
}

// Vault returns a Vault handle whose transit key is provisioned freshly
// for this test (named from a hash of t.Name() so tests stay isolated
// even under -parallel). The container is process-shared via sync.Once
// for speed; only the transit key itself is per-test.
//
// The key is deleted on t.Cleanup so the dev Vault doesn't accumulate
// keys across a large test suite. Deletion requires the key to be
// configured with deletion_allowed=true, which we set at creation time.
func Vault(t *testing.T, ctx context.Context) *VaultHandle {
	t.Helper()
	base, err := ensureVaultBase(ctx)
	if err != nil {
		t.Fatalf("testutil.Vault: %v", err)
	}

	// Per-test transit key name derived from t.Name(). FNV-64a hash
	// keeps the name well under Vault's 255-char limit and turns any
	// "/", " ", etc. in t.Name() into a safe hex digit string.
	h := fnv.New64a()
	_, _ = h.Write([]byte(t.Name())) // hash.Hash.Write never errors
	keyName := fmt.Sprintf("test-key-%x", h.Sum64())

	if _, err := base.client.Logical().WriteWithContext(ctx,
		fmt.Sprintf("%s/keys/%s", vaultTransitMount, keyName),
		map[string]any{"type": "aes256-gcm96"},
	); err != nil {
		t.Fatalf("testutil.Vault: create transit key %s: %v", keyName, err)
	}

	// Allow deletion so cleanup can drop the key. Without this Vault
	// refuses /sys/keys/<name> DELETE with "deletion is not allowed".
	if _, err := base.client.Logical().WriteWithContext(ctx,
		fmt.Sprintf("%s/keys/%s/config", vaultTransitMount, keyName),
		map[string]any{"deletion_allowed": true},
	); err != nil {
		t.Fatalf("testutil.Vault: allow deletion on %s: %v", keyName, err)
	}

	t.Cleanup(func() {
		if _, err := base.client.Logical().DeleteWithContext(ctx,
			fmt.Sprintf("%s/keys/%s", vaultTransitMount, keyName),
		); err != nil {
			t.Logf("testutil.Vault cleanup: delete %s: %v", keyName, err)
		}
	})

	return &VaultHandle{
		Address:      base.address,
		Token:        vaultRootToken,
		TransitMount: vaultTransitMount,
		TransitKey:   keyName,
		client:       base.client,
	}
}

// Rotate triggers a transit-key rotation on this handle's per-test key,
// producing a new key version. Existing ciphertext continues to decrypt;
// new encryptions use the latest version.
func (v *VaultHandle) Rotate(ctx context.Context) error {
	_, err := v.client.Logical().WriteWithContext(ctx,
		fmt.Sprintf("%s/keys/%s/rotate", v.TransitMount, v.TransitKey),
		map[string]any{},
	)
	if err != nil {
		return fmt.Errorf("rotate transit key %s: %w", v.TransitKey, err)
	}
	return nil
}
