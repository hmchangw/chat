package main

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/roomkeymetrics"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

func TestExceedsMaxRedeliver(t *testing.T) {
	tests := []struct {
		name         string
		numDelivered uint64
		maxRedeliver int
		want         bool
	}{
		{name: "below threshold", numDelivered: 5, maxRedeliver: 10, want: false},
		{name: "at threshold (terminate)", numDelivered: 10, maxRedeliver: 10, want: true},
		{name: "above threshold (terminate)", numDelivered: 15, maxRedeliver: 10, want: true},
		{name: "first delivery never terminates", numDelivered: 1, maxRedeliver: 10, want: false},
		{name: "zero delivered (never terminates)", numDelivered: 0, maxRedeliver: 10, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := exceedsMaxRedeliver(tc.numDelivered, tc.maxRedeliver)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestReplicationTerminated_MetricIsNonNil verifies the counter is initialized.
func TestReplicationTerminated_MetricIsNonNil(t *testing.T) {
	assert.NotNil(t, roomkeymetrics.ReplicationTerminated, "ReplicationTerminated metric must be non-nil")
}

func TestBuildConsumerConfig(t *testing.T) {
	siteID := "site-a"

	t.Run("propagates settings", func(t *testing.T) {
		cc := buildConsumerConfig(stream.ConsumerSettings{
			AckWait:       30 * time.Second,
			MaxDeliver:    5,
			MaxWaiting:    512,
			MaxAckPending: 1000,
		}, siteID)

		assert.Equal(t, "inbox-worker", cc.Durable)
		assert.Equal(t, 1000, cc.MaxAckPending)
		assert.Equal(t, []string{subject.InboxAggregateAll(siteID)}, cc.FilterSubjects)
		assert.Equal(t, jetstream.AckExplicitPolicy, cc.AckPolicy)
		assert.Equal(t, 30*time.Second, cc.AckWait)
		assert.Equal(t, 5, cc.MaxDeliver)
		assert.Equal(t, 512, cc.MaxWaiting)
		assert.Equal(t, jetstream.DeliverAllPolicy, cc.DeliverPolicy)
	})

	t.Run("overrides flow through", func(t *testing.T) {
		cc := buildConsumerConfig(stream.ConsumerSettings{
			AckWait:       45 * time.Second,
			MaxDeliver:    3,
			MaxWaiting:    256,
			MaxAckPending: 100,
		}, siteID)

		assert.Equal(t, "inbox-worker", cc.Durable)
		assert.Equal(t, 100, cc.MaxAckPending)
		assert.Equal(t, []string{subject.InboxAggregateAll(siteID)}, cc.FilterSubjects)
		assert.Equal(t, 45*time.Second, cc.AckWait)
		assert.Equal(t, 3, cc.MaxDeliver)
		assert.Equal(t, 256, cc.MaxWaiting)
	})
}
