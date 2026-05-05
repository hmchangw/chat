// Package atrest provides envelope encryption of message payloads at rest.
//
// Each room owns a single 256-bit Data Encryption Key (DEK) used with
// AES-256-GCM to encrypt a JSON-serialised payload. Each DEK is itself
// wrapped (encrypted) with a versioned Key Encryption Key (KEK) loaded from
// a Kubernetes Secret mounted as a JSON file. The wrapped DEK is stored in
// MongoDB; only the unwrapped form is held in process memory.
package atrest

import (
	"errors"
	"time"

	"github.com/hmchangw/chat/pkg/model/cassandra"
)

// EncryptedFields is the bundle of user-authored content that gets
// serialised, encrypted and stored in the Cassandra `enc_payload` column.
// Field names mirror the plaintext columns so that callers can construct
// this struct directly from a cassandra.Message.
type EncryptedFields struct {
	Msg                 string                 `json:"msg,omitempty"`
	Attachments         [][]byte               `json:"attachments,omitempty"`
	Card                *cassandra.Card        `json:"card,omitempty"`
	CardAction          *cassandra.CardAction  `json:"cardAction,omitempty"`
	SysMsgData          []byte                 `json:"sysMsgData,omitempty"`
	QuotedParentContent *QuotedParentEncrypted `json:"quotedParentContent,omitempty"`
}

// QuotedParentEncrypted holds the user-authored fields of a quoted parent
// message. Mentions, sender, timestamps and IDs stay plaintext on the
// quoted_parent_message UDT.
type QuotedParentEncrypted struct {
	Msg         string   `json:"msg,omitempty"`
	Attachments [][]byte `json:"attachments,omitempty"`
}

// EncMeta is the per-row metadata stored alongside the ciphertext.
// kek_version is intentionally absent: the authoritative KEK version is on
// the room's DEK row in MongoDB.
//
// Note: this is the crypto-API form of the type. There is also a
// cassandra.EncMeta sibling type (added in a later task) carrying the
// `cql:""` tags needed for gocql binding. The two have identical content
// and are converted via a one-line struct literal at message-worker /
// history-service boundaries.
type EncMeta struct {
	Nonce []byte `json:"nonce"`
}

// RoomDataKey is the wrapped DEK record stored in MongoDB.
type RoomDataKey struct {
	ID         string    `bson:"_id"`
	WrappedDEK []byte    `bson:"wrappedDEK"`
	WrapNonce  []byte    `bson:"wrapNonce"`
	KEKVersion int       `bson:"kekVersion"`
	CreatedAt  time.Time `bson:"createdAt"`
}

// Config is parsed via caarlos0/env in each consuming service.
type Config struct {
	Enabled      bool          `env:"ATREST_ENABLED"          envDefault:"true"`
	KEKFile      string        `env:"ATREST_KEK_FILE"         envDefault:"/etc/chat/keks.json"`
	DEKCacheSize int           `env:"ATREST_DEK_CACHE_SIZE"   envDefault:"10000"`
	DEKCacheTTL  time.Duration `env:"ATREST_DEK_CACHE_TTL"    envDefault:"1h"`
	ReloadEvery  time.Duration `env:"ATREST_KEK_RELOAD_EVERY" envDefault:"30s"`
}

// Sentinel errors. Callers use errors.Is to identify a class.
var (
	// ErrKEKVersionUnknown means a wrapped DEK references a KEK version
	// that is not present in the loaded key set.
	ErrKEKVersionUnknown = errors.New("atrest: KEK version unknown")
	// ErrAuthFailed means the GCM authentication tag did not validate
	// during decrypt — the ciphertext was tampered with or the wrong key
	// was used.
	ErrAuthFailed = errors.New("atrest: authentication failed")
	// ErrPayloadMalformed means JSON unmarshal of the decrypted payload
	// failed.
	ErrPayloadMalformed = errors.New("atrest: payload malformed")
	// ErrKEKFileInvalid means the KEK secret file failed schema or content
	// validation at load time.
	ErrKEKFileInvalid = errors.New("atrest: KEK file invalid")
)
