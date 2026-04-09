package msgcrypto

//go:generate mockgen -destination=mock_keystore_test.go -package=msgcrypto github.com/hmchangw/chat/pkg/msgkeystore KeyStore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/hmchangw/chat/pkg/msgkeystore"
)

// Encryptor encrypts and decrypts message content using AES-256-GCM
// with per-room versioned keys from a KeyStore.
type Encryptor struct {
	keys       msgkeystore.KeyStore
	randReader io.Reader
}

// NewEncryptor creates an Encryptor that reads keys from the given KeyStore.
func NewEncryptor(keys msgkeystore.KeyStore) *Encryptor {
	return &Encryptor{
		keys:       keys,
		randReader: rand.Reader,
	}
}

// Encrypt encrypts content using the current DB encryption key for roomID.
// Returns a string in the format "enc:v<version>:<base64(nonce+ciphertext+tag)>".
func (e *Encryptor) Encrypt(ctx context.Context, roomID, content string) (string, error) {
	vk, err := e.keys.Get(ctx, roomID)
	if err != nil {
		return "", fmt.Errorf("fetching db key: %w", err)
	}
	if vk == nil {
		return "", fmt.Errorf("no db encryption key for room %s", roomID)
	}

	block, err := aes.NewCipher(vk.Key)
	if err != nil {
		return "", fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating GCM wrapper: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(e.randReader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}

	// Seal prepends nonce to ciphertext+tag because dst == nonce.
	sealed := gcm.Seal(nonce, nonce, []byte(content), nil)

	return fmt.Sprintf("enc:v%d:%s", vk.Version, base64.StdEncoding.EncodeToString(sealed)), nil
}

// Decrypt decrypts content that was encrypted by Encrypt.
// If content does not have the "enc:" prefix, it is returned as-is (plaintext passthrough).
func (e *Encryptor) Decrypt(ctx context.Context, roomID, content string) (string, error) {
	if !strings.HasPrefix(content, "enc:") {
		return content, nil
	}

	// Parse "enc:v<N>:<data>"
	parts := strings.SplitN(content, ":", 3)
	if len(parts) < 3 || !strings.HasPrefix(parts[1], "v") {
		return "", fmt.Errorf("malformed encrypted content: expected enc:v<N>:<data>")
	}

	versionStr := strings.TrimPrefix(parts[1], "v")
	version, err := strconv.Atoi(versionStr)
	if err != nil {
		return "", fmt.Errorf("malformed encrypted content: invalid version %q: %w", versionStr, err)
	}

	data := parts[2]
	key, err := e.keys.GetByVersion(ctx, roomID, version)
	if err != nil {
		return "", fmt.Errorf("fetching db key version %d: %w", version, err)
	}
	if key == nil {
		return "", fmt.Errorf("db encryption key version %d not found for room %s", version, roomID)
	}

	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("decoding base64 payload: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating GCM wrapper: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypting content: %w", err)
	}

	return string(plaintext), nil
}
