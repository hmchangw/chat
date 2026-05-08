package stream

import (
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Project-wide defaults for durable JetStream consumers. Exported so
// individual services can reference them in tests and documentation.
const (
	DefaultAckWait    = 30 * time.Second
	DefaultMaxDeliver = 5
	DefaultMaxWaiting = 512 // NATS 2.10 default
)

// DurableConsumerDefaults returns a ConsumerConfig populated with the
// project-wide standard knobs for durable JetStream consumers.
//
// Callers MUST set Durable. Callers SHOULD set MaxAckPending sized for
// their service's pull concurrency, and FilterSubjects if they need to
// scope the consumer to a subset of the stream's subjects.
//
// DeliverPolicy is honored only at consumer creation. Updating an
// existing durable via js.CreateOrUpdateConsumer does not reset its
// cursor position.
func DurableConsumerDefaults() jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       DefaultAckWait,
		MaxDeliver:    DefaultMaxDeliver,
		MaxWaiting:    DefaultMaxWaiting,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	}
}
