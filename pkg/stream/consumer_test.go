package stream_test

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/stream"
)

func TestDurableConsumerDefaults(t *testing.T) {
	cc := stream.DurableConsumerDefaults()

	assert.Equal(t, jetstream.AckExplicitPolicy, cc.AckPolicy, "AckPolicy")
	assert.Equal(t, 30*time.Second, cc.AckWait, "AckWait")
	assert.Equal(t, 5, cc.MaxDeliver, "MaxDeliver")
	assert.Equal(t, 512, cc.MaxWaiting, "MaxWaiting")
	assert.Equal(t, jetstream.DeliverNewPolicy, cc.DeliverPolicy, "DeliverPolicy")

	// Caller-owned fields are intentionally zero.
	assert.Empty(t, cc.Durable, "Durable must be set by caller")
	assert.Zero(t, cc.MaxAckPending, "MaxAckPending must be set by caller")
	assert.Empty(t, cc.FilterSubjects, "FilterSubjects must be set by caller if needed")
}

func TestDurableConsumerDefaultsConstants(t *testing.T) {
	assert.Equal(t, 30*time.Second, stream.DefaultAckWait)
	assert.Equal(t, 5, stream.DefaultMaxDeliver)
	assert.Equal(t, 512, stream.DefaultMaxWaiting)
}
