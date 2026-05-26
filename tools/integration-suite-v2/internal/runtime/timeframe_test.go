package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
)

func TestTimeframeCloser_QuietPeriodAfterLastEventCloses(t *testing.T) {
	events := make(chan readers.Event, 4)
	now := time.Now()
	traceID := "abc123"
	events <- readers.Event{Location: "reply", Timestamp: now, Traceparent: "00-" + traceID + "-x-01"}
	events <- readers.Event{Location: "mongo.rooms", Timestamp: now.Add(50 * time.Millisecond), Traceparent: "00-" + traceID + "-x-01"}

	gathered, closed := GatherUntilQuiet(events, traceID, []string{"room-service"}, 200*time.Millisecond, 5*time.Second)
	assert.Len(t, gathered, 2)
	assert.True(t, closed.After(now))
}

func TestTimeframeCloser_SafetyCap_NoEventsAtAll(t *testing.T) {
	events := make(chan readers.Event)
	start := time.Now()
	_, closed := GatherUntilQuiet(events, "trace1", []string{"room-service"}, 50*time.Millisecond, 200*time.Millisecond)
	elapsed := closed.Sub(start)
	assert.True(t, elapsed >= 200*time.Millisecond, "safety cap should fire at ~200ms; got %v", elapsed)
}
