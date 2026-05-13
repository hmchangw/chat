//go:build e2e

package e2e

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/hkdf"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomcrypto"
	"github.com/hmchangw/chat/pkg/roomkeystore"
	"github.com/hmchangw/chat/pkg/subject"
)

// Drives the encrypted broadcast path end-to-end against
// broadcast-worker-a-enc: seed a P-256 key in valkey, send a message, find
// the encrypted RoomEvent on the room subject, decrypt with the seeded key.
func TestEncryption_OnSmoke(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

	// Fresh room so the seeded key is unique and doesn't collide with any
	// other test's room (key keyed by roomID in valkey).
	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, site)}, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), bob.Account, roomID)

	// Seed a P-256 key in valkey; broadcast-worker-a-enc reads via roomkeystore.Get.
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	pair := roomkeystore.RoomKeyPair{
		PublicKey:  priv.PublicKey().Bytes(),
		PrivateKey: priv.Bytes(),
	}
	site.SeedRoomKey(t, roomID, pair)
	t.Cleanup(func() {
		// best-effort cleanup; leaving the key behind is harmless.
		site.DeleteRoomKey(t, roomID)
	})

	// 4. Subscribe to room broadcasts BEFORE sending. Both workers publish
	// to the same subject so we'll see two events per message.
	bobSub, err := bob.Conn().SubscribeSync(subject.RoomEvent(roomID))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bobSub.Unsubscribe() })

	// 3. Send a message with a unique body so we can identify it across
	// the system-event noise (e.g. room_created).
	msgID := idgen.GenerateMessageID()
	reqID := idgen.GenerateRequestID()
	body := "encryption smoke " + t.Name()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   body,
			RequestID: reqID,
		},
		10*time.Second,
	))

	// Drain broadcasts; pick the encrypted variant for our msgID.
	deadline := time.Now().Add(10 * time.Second)
	var plaintext string
	var sawEncrypted bool
	for time.Now().Before(deadline) {
		raw, mErr := bobSub.NextMsg(2 * time.Second)
		if mErr != nil {
			break
		}
		var evt model.RoomEvent
		if jerr := json.Unmarshal(raw.Data, &evt); jerr != nil {
			continue
		}
		if len(evt.EncryptedMessage) == 0 {
			continue
		}
		// Message must be nil on the encrypted variant (handler.go:127).
		require.Nil(t, evt.Message,
			"RoomEvent with EncryptedMessage must have Message==nil")
		var env roomcrypto.EncryptedMessage
		require.NoError(t, json.Unmarshal(evt.EncryptedMessage, &env),
			"EncryptedMessage must decode as roomcrypto.EncryptedMessage")
		assert.Equal(t, 0, env.Version,
			"freshly-seeded key has version 0; ciphertext must reference it")

		decrypted, derr := decryptForTest(&env, pair.PrivateKey)
		require.NoError(t, derr, "decrypt with our seeded private key must succeed")

		var clientMsg model.ClientMessage
		require.NoError(t, json.Unmarshal([]byte(decrypted), &clientMsg))
		if clientMsg.ID != msgID {
			// system event encrypted variant (e.g. room_created); not ours
			continue
		}
		assert.Equal(t, body, clientMsg.Content,
			"decrypted content must match what alice sent")
		plaintext = decrypted
		sawEncrypted = true
		break
	}
	require.True(t, sawEncrypted,
		"no encrypted RoomEvent for msgID=%s observed within 10s; "+
			"is broadcast-worker-a-enc running with DURABLE_NAME=broadcast-worker-enc + ENCRYPTION_ENABLED=true?",
		msgID)
	t.Logf("decrypted plaintext: %s", plaintext)
}

// Flips a byte in the captured ciphertext and asserts gcm.Open rejects it.
// Catches a regression that breaks the AEAD binding between key and tag.
func TestEncryption_TamperedCiphertextRejected(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, site)}, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), bob.Account, roomID)

	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	pair := roomkeystore.RoomKeyPair{
		PublicKey:  priv.PublicKey().Bytes(),
		PrivateKey: priv.Bytes(),
	}
	site.SeedRoomKey(t, roomID, pair)
	t.Cleanup(func() { site.DeleteRoomKey(t, roomID) })

	bobSub, err := bob.Conn().SubscribeSync(subject.RoomEvent(roomID))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bobSub.Unsubscribe() })

	msgID := idgen.GenerateMessageID()
	reqID := idgen.GenerateRequestID()
	body := "tamper test " + t.Name()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   body,
			RequestID: reqID,
		},
		10*time.Second,
	))

	// Capture the encrypted broadcast for our msgID.
	deadline := time.Now().Add(10 * time.Second)
	var env roomcrypto.EncryptedMessage
	var found bool
	for time.Now().Before(deadline) {
		raw, mErr := bobSub.NextMsg(2 * time.Second)
		if mErr != nil {
			break
		}
		var evt model.RoomEvent
		if jerr := json.Unmarshal(raw.Data, &evt); jerr != nil {
			continue
		}
		if len(evt.EncryptedMessage) == 0 {
			continue
		}
		require.Nil(t, evt.Message,
			"RoomEvent with EncryptedMessage must have Message==nil")
		var candidate roomcrypto.EncryptedMessage
		if jerr := json.Unmarshal(evt.EncryptedMessage, &candidate); jerr != nil {
			continue
		}
		// Decrypt-and-match approach mirrors the smoke test: we can't know
		// our msgID without decrypting the payload (the wire envelope is
		// opaque ciphertext + ECDH ephemeral + GCM nonce).
		decrypted, derr := decryptForTest(&candidate, pair.PrivateKey)
		if derr != nil {
			continue
		}
		var clientMsg model.ClientMessage
		if jerr := json.Unmarshal([]byte(decrypted), &clientMsg); jerr != nil {
			continue
		}
		if clientMsg.ID != msgID {
			continue
		}
		env = candidate
		found = true
		break
	}
	require.True(t, found,
		"no encrypted RoomEvent for msgID=%s observed within 10s", msgID)

	// Sanity check: the untampered envelope decrypts cleanly. Without this
	// baseline, a tampering failure could be confused with a setup failure
	// (e.g. wrong key, missing ephemeral pubkey).
	cleartext, err := decryptForTest(&env, pair.PrivateKey)
	require.NoError(t, err, "baseline decrypt of untampered envelope must succeed")
	require.Contains(t, cleartext, body)

	// Deep-copy before tampering: NextMsg returns shared []byte slices.
	require.NotEmpty(t, env.Ciphertext, "ciphertext must be non-empty to tamper")
	tampered := roomcrypto.EncryptedMessage{
		Version:            env.Version,
		EphemeralPublicKey: append([]byte(nil), env.EphemeralPublicKey...),
		Nonce:              append([]byte(nil), env.Nonce...),
		Ciphertext:         append([]byte(nil), env.Ciphertext...),
	}
	tampered.Ciphertext[0] ^= 0x01

	_, err = decryptForTest(&tampered, pair.PrivateKey)
	require.Error(t, err,
		"tampered ciphertext MUST be rejected by gcm.Open — if this passes, "+
			"the AEAD authentication is broken (HKDF parameters, AES key size, "+
			"or curve has drifted from the encoder)")

	// Same expectation for a flipped nonce byte.
	tamperedNonce := roomcrypto.EncryptedMessage{
		Version:            env.Version,
		EphemeralPublicKey: append([]byte(nil), env.EphemeralPublicKey...),
		Nonce:              append([]byte(nil), env.Nonce...),
		Ciphertext:         append([]byte(nil), env.Ciphertext...),
	}
	tamperedNonce.Nonce[0] ^= 0x01
	_, err = decryptForTest(&tamperedNonce, pair.PrivateKey)
	require.Error(t, err,
		"tampered nonce MUST also fail GCM authentication")
}

// Mirrors pkg/roomcrypto + broadcast-worker/testhelpers_test.go (can't
// import _test.go across packages). Keep in lockstep with Encode().
func decryptForTest(env *roomcrypto.EncryptedMessage, roomPrivateKey []byte) (string, error) {
	privKey, err := ecdh.P256().NewPrivateKey(roomPrivateKey)
	if err != nil {
		return "", err
	}
	ephPub, err := ecdh.P256().NewPublicKey(env.EphemeralPublicKey)
	if err != nil {
		return "", err
	}
	shared, err := privKey.ECDH(ephPub)
	if err != nil {
		return "", err
	}
	aesKey := make([]byte, 32)
	r := hkdf.New(sha256.New, shared, nil, []byte("room-message-encryption"))
	if _, err := io.ReadFull(r, aesKey); err != nil {
		return "", err
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	pt, err := gcm.Open(nil, env.Nonce, env.Ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
