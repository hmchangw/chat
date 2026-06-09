package atrest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewVaultKeyWrapper_AppRoleRequiresSecretIDFile verifies that selecting
// AppRole auth (RoleID set) without a secret-ID file fails closed at
// construction with an actionable, env-var-named error — before any network
// call to Vault.
func TestNewVaultKeyWrapper_AppRoleRequiresSecretIDFile(t *testing.T) {
	_, err := NewVaultKeyWrapper(context.Background(), VaultConfig{
		Address:    "http://127.0.0.1:8200",
		TransitKey: "chat-kek",
		AppRoleID:  "11111111-2222-3333-4444-555555555555",
		// AppRoleSecretIDFile intentionally left empty.
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VAULT_APPROLE_SECRET_ID_FILE")
}

// TestNewVaultKeyWrapper_NoCredentials verifies the default branch still
// rejects a config with no auth method selected, and that the error mentions
// every supported method so operators know their options.
func TestNewVaultKeyWrapper_NoCredentials(t *testing.T) {
	_, err := NewVaultKeyWrapper(context.Background(), VaultConfig{
		Address:    "http://127.0.0.1:8200",
		TransitKey: "chat-kek",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VAULT_K8S_ROLE")
	assert.Contains(t, err.Error(), "VAULT_APPROLE_ROLE_ID")
	assert.Contains(t, err.Error(), "VAULT_TOKEN")
}
