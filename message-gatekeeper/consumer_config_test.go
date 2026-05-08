package main

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
)

func TestBuildConsumerConfig(t *testing.T) {
	cc := buildConsumerConfig()

	assert.Equal(t, "message-gatekeeper", cc.Durable)
	assert.Equal(t, 1000, cc.MaxAckPending)
	assert.Equal(t, jetstream.AckExplicitPolicy, cc.AckPolicy)
	assert.Equal(t, 30*time.Second, cc.AckWait)
	assert.Equal(t, 5, cc.MaxDeliver)
	assert.Equal(t, 512, cc.MaxWaiting)
	assert.Equal(t, jetstream.DeliverNewPolicy, cc.DeliverPolicy)
}
