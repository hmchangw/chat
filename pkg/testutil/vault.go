//go:build integration

package testutil

import (
	"context"
	"fmt"
	"sync"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
	tcvault "github.com/testcontainers/testcontainers-go/modules/vault"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

// VaultHandle is the connection details for a shared dev-mode Vault
// container with the transit engine pre-configured.
type VaultHandle struct {
	Address      string
	Token        string
	TransitMount string
	TransitKey   string

	client *vaultapi.Client
}

const (
	vaultRootToken     = "test-root-token"
	vaultTransitMount  = "transit"
	vaultTransitKey    = "atrest-test-key"
	vaultContainerSlug = "vault-test"
)

var (
	vaultOnce    sync.Once
	vaultHandle  *VaultHandle
	vaultInitErr error
)

func ensureVault(ctx context.Context) (*VaultHandle, error) {
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

		// Enable the transit engine and create the named key.
		if err := client.Sys().MountWithContext(ctx, vaultTransitMount, &vaultapi.MountInput{
			Type: "transit",
		}); err != nil {
			vaultInitErr = fmt.Errorf("mount transit: %w", err)
			return
		}
		if _, err := client.Logical().WriteWithContext(ctx,
			fmt.Sprintf("%s/keys/%s", vaultTransitMount, vaultTransitKey),
			map[string]any{"type": "aes256-gcm96"},
		); err != nil {
			vaultInitErr = fmt.Errorf("create transit key: %w", err)
			return
		}

		vaultHandle = &VaultHandle{
			Address:      addr,
			Token:        vaultRootToken,
			TransitMount: vaultTransitMount,
			TransitKey:   vaultTransitKey,
			client:       client,
		}
	})
	return vaultHandle, vaultInitErr
}

// Vault returns a shared Vault dev container with the transit engine
// mounted at "transit" and a single AES-256-GCM key created. The
// container is started once per test process via sync.Once and reused
// across all tests that call this helper.
func Vault(t *testing.T, ctx context.Context) *VaultHandle {
	t.Helper()
	v, err := ensureVault(ctx)
	if err != nil {
		t.Fatalf("testutil.Vault: %v", err)
	}
	return v
}

// Rotate triggers a transit-key rotation on the shared key, producing a
// new key version. Existing ciphertext continues to decrypt; new
// encryptions use the latest version.
func (v *VaultHandle) Rotate(ctx context.Context) error {
	_, err := v.client.Logical().WriteWithContext(ctx,
		fmt.Sprintf("%s/keys/%s/rotate", v.TransitMount, v.TransitKey),
		map[string]any{},
	)
	if err != nil {
		return fmt.Errorf("rotate transit key: %w", err)
	}
	return nil
}
