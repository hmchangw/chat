package atrest

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"

	vault "github.com/hashicorp/vault/api"
	authapprole "github.com/hashicorp/vault/api/auth/approle"
	authk8s "github.com/hashicorp/vault/api/auth/kubernetes"
)

// observeWatcher reads the lifetime watcher's terminal status and logs
// + increments a metric on a renewal failure. Without this the goroutine
// running watcher.Start() exits silently when Vault is unreachable
// across a renewal window, the token hits max_ttl, or the role's
// policy is revoked — every subsequent Wrap/Unwrap then returns 403
// with no signal for operators. A clean Stop() (which we do during
// graceful shutdown) emits a nil error and is not logged.
func observeWatcher(watcher *vault.LifetimeWatcher) {
	err := <-watcher.DoneCh()
	if err == nil {
		return
	}
	kekRenewalFailures.Inc()
	slog.Error("atrest: vault token renewal stopped with error", "error", err)
}

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

	// AppRoleID is the RoleID for AppRole auth. When set (and K8sRole is
	// empty), the service logs in via AppRole using the secret ID read from
	// AppRoleSecretIDFile. Suited to non-Kubernetes production deployments.
	AppRoleID string `env:"VAULT_APPROLE_ROLE_ID" envDefault:""`

	// AppRoleSecretIDFile is the path to a file holding the AppRole secret
	// ID. File-based delivery keeps the secret out of the process
	// environment (and out of child processes, crash dumps, and `kubectl
	// describe`), and lets an orchestrator rotate it without a restart — the
	// helper re-reads the file on each login. Required when AppRoleID is set.
	AppRoleSecretIDFile string `env:"VAULT_APPROLE_SECRET_ID_FILE" envDefault:""`

	// AppRoleAuthPath is the AppRole auth method's mount path (default
	// "approle").
	AppRoleAuthPath string `env:"VAULT_APPROLE_AUTH_PATH" envDefault:"approle"`

	// Token is the static Vault token used when no other auth method is
	// selected. Suitable for local docker-compose only; production should
	// use Kubernetes or AppRole auth.
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

// loginWithRenewal performs an auth-method login and starts a background
// lifetime watcher so a long-lived process keeps renewing its token instead
// of dropping auth on TTL expiry. The returned watcher is stored on the
// wrapper and stopped during graceful shutdown via Close. observeWatcher
// surfaces a silently-stopped renewal in logs and metrics. Shared by the
// Kubernetes and AppRole branches, which differ only in how they build the
// vault.AuthMethod.
func loginWithRenewal(ctx context.Context, client *vault.Client, method vault.AuthMethod) (*vault.LifetimeWatcher, error) {
	secret, err := client.Auth().Login(ctx, method)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	if secret == nil || secret.Auth == nil {
		return nil, errors.New("login returned empty auth")
	}
	watcher, err := client.NewLifetimeWatcher(&vault.LifetimeWatcherInput{Secret: secret})
	if err != nil {
		return nil, fmt.Errorf("lifetime watcher: %w", err)
	}
	go watcher.Start()
	go observeWatcher(watcher)
	return watcher, nil
}

// NewVaultKeyWrapper constructs a KeyWrapper backed by Vault's transit
// engine. It selects an auth method by precedence: Kubernetes auth when
// cfg.K8sRole is set, then AppRole when cfg.AppRoleID is set, then a static
// cfg.Token. Returns an error if the resulting client cannot reach Vault or
// has no usable credentials.
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

	var watcher *vault.LifetimeWatcher
	switch {
	case cfg.K8sRole != "":
		k8sAuth, err := authk8s.NewKubernetesAuth(cfg.K8sRole, authk8s.WithMountPath(cfg.K8sAuthPath))
		if err != nil {
			return nil, fmt.Errorf("vault: configure kubernetes auth: %w", err)
		}
		watcher, err = loginWithRenewal(ctx, client, k8sAuth)
		if err != nil {
			return nil, fmt.Errorf("vault: kubernetes %w", err)
		}
	case cfg.AppRoleID != "":
		// The secret ID is the sensitive half of the AppRole credential and
		// is only ever sourced from a file — never an env var — so it stays
		// out of the process environment. The helper reads the file lazily
		// at each login, which also makes secret-ID rotation a file rewrite
		// rather than a restart.
		if cfg.AppRoleSecretIDFile == "" {
			return nil, errors.New("vault: VAULT_APPROLE_SECRET_ID_FILE is required when VAULT_APPROLE_ROLE_ID is set")
		}
		// Only override the mount path when explicitly set — an empty value
		// would build a broken "auth//login" path. Leaving the option off
		// lets the helper apply its own "approle" default, matching our
		// envDefault.
		opts := []authapprole.LoginOption{}
		if cfg.AppRoleAuthPath != "" {
			opts = append(opts, authapprole.WithMountPath(cfg.AppRoleAuthPath))
		}
		appRoleAuth, err := authapprole.NewAppRoleAuth(
			cfg.AppRoleID,
			&authapprole.SecretID{FromFile: cfg.AppRoleSecretIDFile},
			opts...,
		)
		if err != nil {
			return nil, fmt.Errorf("vault: configure approle auth: %w", err)
		}
		watcher, err = loginWithRenewal(ctx, client, appRoleAuth)
		if err != nil {
			return nil, fmt.Errorf("vault: approle %w", err)
		}
	case cfg.Token != "":
		client.SetToken(cfg.Token)
		// Validate the token + connectivity at construction time so a
		// misconfigured deploy fails closed at startup rather than at the
		// first Wrap/Unwrap call. Matches the login branches above which
		// fail immediately on a bad credential.
		if _, err := client.Auth().Token().LookupSelfWithContext(ctx); err != nil {
			return nil, fmt.Errorf("vault: validate static token: %w", err)
		}
	default:
		return nil, errors.New("vault: one of VAULT_K8S_ROLE, VAULT_APPROLE_ROLE_ID, or VAULT_TOKEN must be set")
	}

	return &vaultKeyWrapper{
		client:       client,
		transitMount: cfg.TransitMount,
		transitKey:   cfg.TransitKey,
		watcher:      watcher,
	}, nil
}

// GenerateDataKey asks Vault to mint a fresh 256-bit DEK via the transit
// engine's datakey endpoint and return both the plaintext DEK and its
// KEK-wrapped form in one round-trip. In HSM-backed Vault deployments
// the DEK is generated inside the HSM, so the plaintext material
// originates there rather than in this process — the property our
// security review asked for.
func (w *vaultKeyWrapper) GenerateDataKey(ctx context.Context) (plaintext, wrapped []byte, err error) {
	defer func() { kekWrapCounter.WithLabelValues(resultLabel(err)).Inc() }()
	resp, err := w.client.Logical().WriteWithContext(ctx,
		fmt.Sprintf("%s/datakey/plaintext/%s", w.transitMount, w.transitKey),
		map[string]any{"bits": 256},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("vault transit datakey: %w", err)
	}
	if resp == nil || resp.Data == nil {
		return nil, nil, errors.New("vault transit datakey: empty response")
	}
	b64, ok := resp.Data["plaintext"].(string)
	if !ok || b64 == "" {
		return nil, nil, errors.New("vault transit datakey: missing plaintext")
	}
	dek, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, nil, fmt.Errorf("vault transit datakey: base64 decode: %w", err)
	}
	ct, ok := resp.Data["ciphertext"].(string)
	if !ok || ct == "" {
		return nil, nil, errors.New("vault transit datakey: missing ciphertext")
	}
	return dek, []byte(ct), nil
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
