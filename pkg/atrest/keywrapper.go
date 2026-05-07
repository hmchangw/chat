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
// Wrap encrypts a 32-byte DEK and returns an opaque ciphertext blob.
// Unwrap reverses the operation. The blob format is implementation-
// specific (Vault returns "vault:vN:..." strings, for example) and
// callers MUST treat it as opaque bytes.
type KeyWrapper interface {
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
