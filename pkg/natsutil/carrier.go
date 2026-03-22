package natsutil

import "github.com/nats-io/nats.go"

// HeaderCarrier implements propagation.TextMapCarrier for NATS headers.
type HeaderCarrier struct {
	hdr *nats.Header
}

func NewHeaderCarrier(hdr *nats.Header) *HeaderCarrier {
	return &HeaderCarrier{hdr: hdr}
}

func (c *HeaderCarrier) Get(key string) string {
	return c.hdr.Get(key)
}

func (c *HeaderCarrier) Set(key, value string) {
	c.hdr.Set(key, value)
}

func (c *HeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(*c.hdr))
	for k := range *c.hdr {
		keys = append(keys, k)
	}
	return keys
}
