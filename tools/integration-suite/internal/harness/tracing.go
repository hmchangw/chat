package harness

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// TraceparentHeader is the W3C Trace Context header name.
const TraceparentHeader = "traceparent"

// NewTraceparent generates a fresh W3C traceparent header value:
//
//	00-<32hex trace id>-<16hex span id>-01
//
// We do not depend on an OTel SDK in v1; this is enough for the
// receiving services to include the trace ID in their spans.
func NewTraceparent() string {
	var traceID [16]byte
	var spanID [8]byte
	if _, err := rand.Read(traceID[:]); err != nil {
		panic(fmt.Sprintf("tracing: NewTraceparent: %v", err))
	}
	if _, err := rand.Read(spanID[:]); err != nil {
		panic(fmt.Sprintf("tracing: NewTraceparent: %v", err))
	}
	return fmt.Sprintf("00-%s-%s-01",
		hex.EncodeToString(traceID[:]),
		hex.EncodeToString(spanID[:]))
}

// TraceIDFromTraceparent extracts the 32-hex trace ID from a W3C
// traceparent header value.
func TraceIDFromTraceparent(tp string) (string, error) {
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		return "", fmt.Errorf("tracing: traceparent has %d parts, expected 4", len(parts))
	}
	if len(parts[1]) != 32 {
		return "", fmt.Errorf("tracing: trace ID has length %d, expected 32", len(parts[1]))
	}
	return parts[1], nil
}
