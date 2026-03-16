package message

import (
	"encoding/json"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Envelope is the enriched message published to Core NATS.
type Envelope struct {
	// Original payload from JetStream
	Payload json.RawMessage `json:"payload"`

	// Enrichment metadata
	Source        string            `json:"source"`
	SourceSubject string            `json:"source_subject"`
	StreamSeq     uint64            `json:"stream_seq"`
	Timestamp     time.Time         `json:"timestamp"`
	BridgedAt     time.Time         `json:"bridged_at"`
	Headers       map[string]string `json:"headers,omitempty"`
}

// Enrich transforms a raw JetStream message into an enriched Envelope
// that includes metadata about origin, timing, and delivery context.
func Enrich(msg jetstream.Msg) ([]byte, error) {
	meta, _ := msg.Metadata()

	env := Envelope{
		Payload:       json.RawMessage(msg.Data()),
		Source:        "jetstream",
		SourceSubject: msg.Subject(),
		BridgedAt:     time.Now().UTC(),
	}

	if meta != nil {
		env.StreamSeq = meta.Sequence.Stream
		env.Timestamp = meta.Timestamp
	}

	if msg.Headers() != nil {
		env.Headers = make(map[string]string, len(msg.Headers()))
		for k, v := range msg.Headers() {
			if len(v) > 0 {
				env.Headers[k] = v[0]
			}
		}
	}

	return json.Marshal(env)
}
