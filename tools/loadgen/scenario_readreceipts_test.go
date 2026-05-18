package main

import (
	"context"
	"fmt"
	randv2 "math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadReceipts_FiresPerCoverage(t *testing.T) {
	// 100 recent messages, coverage=0.6 → expect ~60 read events per pass.
	recents := make([]RecentMessage, 100)
	for i := range recents {
		recents[i] = RecentMessage{MessageID: padID(i), RoomID: "r1", RoomType: "channel"}
	}

	var fired []string
	fakeFn := func(_ context.Context, msgID string, _ string /* roomType */) error {
		fired = append(fired, msgID)
		return nil
	}
	rng := randv2.New(randv2.NewPCG(42, 0))
	err := fireReadReceipts(context.Background(), recents, 0.6, fakeFn, rng)
	require.NoError(t, err)

	// 60% of 100 ± 10 tolerance.
	assert.InDelta(t, 60, len(fired), 10, "expected ~60 events at coverage=0.6; got %d", len(fired))
}

func TestReadReceipts_HonorsZeroCoverage(t *testing.T) {
	recents := []RecentMessage{{MessageID: "m1", RoomID: "r1", RoomType: "channel"}}
	fired := 0
	fakeFn := func(_ context.Context, _ string, _ string) error { fired++; return nil }
	err := fireReadReceipts(context.Background(), recents, 0, fakeFn, randv2.New(randv2.NewPCG(42, 0)))
	require.NoError(t, err)
	assert.Equal(t, 0, fired, "coverage=0 must fire zero events")
}

func TestReadReceipts_RegistersAsScenario(t *testing.T) {
	sc, ok := LookupScenario("read-receipts")
	require.True(t, ok)
	assert.Equal(t, "read-receipts", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestCollector_RecentMessages_Bounded(t *testing.T) {
	c := NewCollector(&Metrics{}, "test")
	// Push more than the cap to ensure the ring wraps.
	for i := 0; i < 2000; i++ {
		c.RecordPublished(RecentMessage{MessageID: padID(i), RoomID: "r", RoomType: "channel"})
	}
	recent := c.RecentMessages(50)
	require.Len(t, recent, 50)
	// Verify they're the NEWEST 50 (IDs 1950..1999).
	assert.Equal(t, padID(1999), recent[len(recent)-1].MessageID)
}

func padID(i int) string {
	return fmt.Sprintf("m%07d", i)
}
