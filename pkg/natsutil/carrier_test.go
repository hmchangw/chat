package natsutil_test

import (
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestHeaderCarrier(t *testing.T) {
	hdr := nats.Header{}
	c := natsutil.NewHeaderCarrier(&hdr)

	c.Set("traceparent", "00-abc-def-01")
	if got := c.Get("traceparent"); got != "00-abc-def-01" {
		t.Errorf("Get = %q", got)
	}

	keys := c.Keys()
	if len(keys) != 1 || keys[0] != "traceparent" {
		t.Errorf("Keys = %v", keys)
	}
}
