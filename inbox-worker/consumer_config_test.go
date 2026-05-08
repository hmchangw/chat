package main

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/subject"
)

func TestBuildConsumerConfig(t *testing.T) {
	siteID := "site-a"
	cc := buildConsumerConfig(siteID)

	assert.Equal(t, "inbox-worker", cc.Durable)
	assert.Equal(t, 100, cc.MaxAckPending)
	assert.Equal(t, []string{subject.InboxAggregateAll(siteID)}, cc.FilterSubjects)
	assert.Equal(t, jetstream.AckExplicitPolicy, cc.AckPolicy)
	assert.Equal(t, 30*time.Second, cc.AckWait)
	assert.Equal(t, 5, cc.MaxDeliver)
	assert.Equal(t, 512, cc.MaxWaiting)
	assert.Equal(t, jetstream.DeliverNewPolicy, cc.DeliverPolicy)
}
