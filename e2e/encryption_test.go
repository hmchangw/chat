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

// TestEncryption_OnSmoke exercises broadcast-worker's ENCRYPTION_ENABLED=true
// code path end-to-end. The compose stack runs TWO siteA broadcast workers:
//
//   - broadcast-worker-a (DURABLE_NAME=broadcast-worker, ENCRYPTION_ENABLED=false)
//     handles every message in the existing 33 e2e tests.
//   - broadcast-worker-a-enc (DURABLE_NAME=broadcast-worker-enc,
//     ENCRYPTION_ENABLED=true) is the variant under test here. It fetches
//     a room key from valkey, encrypts the ClientMessage body via
//     pkg/roomcrypto (P-256 ECDH + HKDF-SHA256 + AES-256-GCM), and publishes
//     a RoomEvent with `Message=nil + EncryptedMessage` populated.
//
// Both workers publish to the SAME subject, so the recipient receives TWO
// broadcasts per message: one plaintext, one encrypted. The test:
//
//  1. Generates a fresh P-256 key pair.
//  2. Seeds it in valkey via roomkeystore (the production interface).
//  3. Sends a message in a fresh room.
//  4. Captures broadcasts on the room subject.
//  5. Asserts at least one carries `EncryptedMessage` (not `Message`),
//     decrypts it with our private key, and verifies the plaintext body
//     matches what was sent.
//
// This proves the END-TO-END crypto path: gatekeeper accepts plaintext,
// canonical worker persists, broadcast-worker-a-enc fetches the room key
// from valkey + encrypts + publishes wire-shaped EncryptedMessage. The
// previous "skipped because no encrypted worker exists" rationale is
// resolved by the new DURABLE_NAME env var (post-R3 production change).
func TestEncryption_OnSmoke(t *testing.T) {
	// Single-site, per-test room + per-test seeded valkey key under a
	// unique roomID. broadcast-worker-a-enc is a shared consumer
	// (durable "broadcast-worker-enc"); each parallel test pulls only
	// its own room's events because the broadcast subject is keyed by
	// roomID. Safe for parallel.
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
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), bob.Account, roomID)

	// 1+2. Generate a P-256 key pair and seed it in valkey via the
	// production RoomKeyStore interface. Key is stored as a hash at
	// `room:{roomID}:key` with version=0; broadcast-worker-a-enc reads
	// it via roomkeystore.Get(roomID) on each message.
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

	// 5. Drain broadcasts looking for the encrypted RoomEvent for our
	// msgID. We may see plaintext + encrypted variants for the same
	// message, plus system events; only the encrypted one with our msgID
	// satisfies the test contract.
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
		// Encrypted variant: Message must be nil per handler.go:127.
		// require (not assert) -- subsequent Unmarshal into
		// roomcrypto.EncryptedMessage is meaningful only when Message==nil;
		// continuing past a non-nil Message produces a confusing downstream
		// failure.
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

// decryptForTest mirrors the production decrypt path in pkg/roomcrypto +
// broadcast-worker/testhelpers_test.go. We can't import _test.go files
// across packages, so the algorithm is duplicated here. If
// pkg/roomcrypto.Encode's algorithm parameters ever change (HKDF info
// string, AES key size, etc.), this function must mirror them in lockstep.
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
