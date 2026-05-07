package atrest

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	vault "github.com/hashicorp/vault/api"
	authk8s "github.com/hashicorp/vault/api/auth/kubernetes"
)

// VaultConfig configures the Vault transit-engine KeyWrapper. It is
// parsed via caarlos0/env in each consuming service.
type VaultConfig struct {
	// Address is the Vault server URL (e.g. https://vault.svc:8200).
	Address string `env:"VAULT_ADDR" envDefault:""`

	// TransitMount is the path the transit secrets engine is mounted at.
	TransitMount string `env:"ATREST_VAULT_TRANSIT_MOUNT" envDefault:"transit"`

	// TransitKey is the named key under that mount used to wrap DEKs
	// (e.g. "chat-kek").
	TransitKey string `env:"ATREST_VAULT_TRANSIT_KEY" envDefault:"chat-kek"`

	// K8sRole is the Vault role to log in as via Kubernetes auth. When
	// set, the service uses its mounted ServiceAccount token to obtain a
	// Vault token. Leave empty for non-k8s deployments and pair with
	// Token.
	K8sRole string `env:"VAULT_K8S_ROLE" envDefault:""`

	// K8sAuthPath is the auth method's mount path (default "kubernetes").
	K8sAuthPath string `env:"VAULT_K8S_AUTH_PATH" envDefault:"kubernetes"`

	// Token is the static Vault token used when K8sRole is empty. Suitable
	// for local docker-compose only; production should use Kubernetes auth.
	Token string `env:"VAULT_TOKEN" envDefault:""`
}

// vaultKeyWrapper wraps DEKs using Vault's transit secrets engine.
type vaultKeyWrapper struct {
	client       *vault.Client
	transitMount string
	transitKey   string
	watcher      *vault.LifetimeWatcher // nil when using a static token
}

// Close stops the background token-renewal goroutine if Kubernetes auth
// is in use. Safe to call repeatedly; safe to call when the wrapper was
// constructed with a static token.
func (w *vaultKeyWrapper) Close() error {
	if w.watcher != nil {
		w.watcher.Stop()
	}
	return nil
}

// NewVaultKeyWrapper constructs a KeyWrapper backed by Vault's transit
// engine. It logs in via Kubernetes auth when cfg.K8sRole is set, falling
// back to cfg.Token otherwise. Returns an error if the resulting client
// cannot reach Vault or has no usable credentials.
func NewVaultKeyWrapper(ctx context.Context, cfg VaultConfig) (*vaultKeyWrapper, error) { //nolint:revive,gocritic // unexported return is intentional (callers consume via KeyWrapper); hugeParam: cfg passed by value matches the project's caarlos0/env config-struct convention
	if cfg.Address == "" {
		return nil, errors.New("vault: VAULT_ADDR is required")
	}
	if cfg.TransitKey == "" {
		return nil, errors.New("vault: ATREST_VAULT_TRANSIT_KEY is required")
	}

	vcfg := vault.DefaultConfig()
	vcfg.Address = cfg.Address
	client, err := vault.NewClient(vcfg)
	if err != nil {
		return nil, fmt.Errorf("vault: new client: %w", err)
	}

	switch {
	case cfg.K8sRole != "":
		k8sAuth, err := authk8s.NewKubernetesAuth(cfg.K8sRole, authk8s.WithMountPath(cfg.K8sAuthPath))
		if err != nil {
			return nil, fmt.Errorf("vault: configure kubernetes auth: %w", err)
		}
		secret, err := client.Auth().Login(ctx, k8sAuth)
		if err != nil {
			return nil, fmt.Errorf("vault: kubernetes login: %w", err)
		}
		if secret == nil || secret.Auth == nil {
			return nil, errors.New("vault: kubernetes login returned empty auth")
		}
		// Renew the token in the background so long-lived processes don't
		// drop their auth on TTL expiry.
		watcher, err := client.NewLifetimeWatcher(&vault.LifetimeWatcherInput{Secret: secret})
		if err != nil {
			return nil, fmt.Errorf("vault: lifetime watcher: %w", err)
		}
		go watcher.Start()
		return &vaultKeyWrapper{
			client:       client,
			transitMount: cfg.TransitMount,
			transitKey:   cfg.TransitKey,
			watcher:      watcher,
		}, nil
	case cfg.Token != "":
		client.SetToken(cfg.Token)
	default:
		return nil, errors.New("vault: either VAULT_K8S_ROLE or VAULT_TOKEN must be set")
	}

	return &vaultKeyWrapper{
		client:       client,
		transitMount: cfg.TransitMount,
		transitKey:   cfg.TransitKey,
	}, nil
}

// Wrap encrypts the DEK via Vault's transit engine and returns the
// "vault:vN:..." ciphertext bytes.
func (w *vaultKeyWrapper) Wrap(ctx context.Context, dek []byte) (out []byte, err error) {
	defer func() { kekWrapCounter.WithLabelValues(resultLabel(err)).Inc() }()
	resp, err := w.client.Logical().WriteWithContext(ctx,
		fmt.Sprintf("%s/encrypt/%s", w.transitMount, w.transitKey),
		map[string]any{
			"plaintext": base64.StdEncoding.EncodeToString(dek),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("vault transit encrypt: %w", err)
	}
	if resp == nil || resp.Data == nil {
		return nil, errors.New("vault transit encrypt: empty response")
	}
	ct, ok := resp.Data["ciphertext"].(string)
	if !ok || ct == "" {
		return nil, errors.New("vault transit encrypt: missing ciphertext")
	}
	return []byte(ct), nil
}

// Unwrap decrypts a "vault:vN:..." ciphertext via Vault's transit engine
// and returns the plaintext DEK.
func (w *vaultKeyWrapper) Unwrap(ctx context.Context, ciphertext []byte) (out []byte, err error) {
	defer func() { kekUnwrapCounter.WithLabelValues(resultLabel(err)).Inc() }()
	resp, err := w.client.Logical().WriteWithContext(ctx,
		fmt.Sprintf("%s/decrypt/%s", w.transitMount, w.transitKey),
		map[string]any{
			"ciphertext": string(ciphertext),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("vault transit decrypt: %w", err)
	}
	if resp == nil || resp.Data == nil {
		return nil, errors.New("vault transit decrypt: empty response")
	}
	b64, ok := resp.Data["plaintext"].(string)
	if !ok || b64 == "" {
		return nil, errors.New("vault transit decrypt: missing plaintext")
	}
	dek, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("vault transit decrypt: base64 decode: %w", err)
	}
	return dek, nil
}
