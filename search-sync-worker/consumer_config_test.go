package main

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
)

type fakeCollection struct {
	name    string
	filters []string
}

func (f fakeCollection) ConsumerName() string             { return f.name }
func (f fakeCollection) FilterSubjects(_ string) []string { return f.filters }

func TestBuildConsumerConfig(t *testing.T) {
	tests := []struct {
		name        string
		coll        fakeCollection
		siteID      string
		wantFilters []string
	}{
		{
			name:        "with filters",
			coll:        fakeCollection{name: "message-sync", filters: []string{"chat.msg.canonical.site-a.created"}},
			siteID:      "site-a",
			wantFilters: []string{"chat.msg.canonical.site-a.created"},
		},
		{
			name:        "without filters",
			coll:        fakeCollection{name: "spotlight-sync", filters: nil},
			siteID:      "site-a",
			wantFilters: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc := buildConsumerConfig(tt.coll, tt.siteID)

			assert.Equal(t, tt.coll.name, cc.Durable)
			assert.Equal(t, 500, cc.MaxAckPending)
			assert.Equal(t, tt.wantFilters, cc.FilterSubjects)
			assert.Equal(t, []time.Duration{1 * time.Second, 5 * time.Second, 30 * time.Second}, cc.BackOff)
			assert.Equal(t, jetstream.AckExplicitPolicy, cc.AckPolicy)
			assert.Equal(t, 30*time.Second, cc.AckWait)
			assert.Equal(t, 5, cc.MaxDeliver)
			assert.Equal(t, 512, cc.MaxWaiting)
			assert.Equal(t, jetstream.DeliverNewPolicy, cc.DeliverPolicy)
		})
	}
}
