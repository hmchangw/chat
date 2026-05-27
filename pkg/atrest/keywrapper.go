package atrest

import (
	"context"
	"io"
)

// KeyWrapper wraps and unwraps a Data Encryption Key (DEK) using a Key
// Encryption Key (KEK) that the wrapper itself owns. The plaintext KEK
// never leaves the wrapper — for the Vault implementation it never
// leaves the Vault server at all.
//
// GenerateDataKey is the preferred path for minting a new DEK: the KEK
// provider produces both the plaintext DEK and its wrapped form in a
// single call, so the DEK material originates inside the KEK provider
// (an HSM, in compliant Vault deployments) rather than in the calling
// process's address space.
//
// Wrap / Unwrap remain for legacy DEKs already persisted with the
// local-random + Wrap flow, and for KEK-rotation re-wrap operations.
// The blob format is implementation-specific (Vault returns
// "vault:vN:..." strings) and callers MUST treat it as opaque bytes.
type KeyWrapper interface {
	GenerateDataKey(ctx context.Context) (plaintext, wrapped []byte, err error)
	Wrap(ctx context.Context, dek []byte) ([]byte, error)
	Unwrap(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// KeyWrapperCloser is the resource-owning variant returned by
// constructors that need explicit cleanup (e.g. the Vault wrapper has a
// background token-renewal goroutine to stop). Services hold this type
// so they can register a graceful-shutdown callback.
type KeyWrapperCloser interface {
	KeyWrapper
	io.Closer
}
