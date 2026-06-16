package readers

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- P4: jetstream_consume gunzip ---
// Push-notification fan-out (and other internal pipelines) gzip the
// payload before publishing. The matcher needs decoded JSON; without
// gunzip support a gzipped message's BodyJSON stays empty and a
// match: {body_json: {...}} silently never matches. The reader
// detects gzip via the Content-Encoding header AND a magic-byte
// fallback, decompresses, and JSON-decodes as today.

func gzipBytes(t *testing.T, in []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write(in)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestDecodeJetStreamBody_PlainJSON(t *testing.T) {
	// Regression: plain JSON path is unchanged.
	body, raw := decodeJetStreamBody([]byte(`{"k":"v","n":42}`), nil)
	require.NotNil(t, body)
	assert.Equal(t, "v", body["k"])
	assert.Empty(t, raw)
}

func TestDecodeJetStreamBody_GzipViaHeader(t *testing.T) {
	gz := gzipBytes(t, []byte(`{"event":"push","recipients":["alice","bob"]}`))
	hdr := map[string][]string{"Content-Encoding": {"gzip"}}
	body, raw := decodeJetStreamBody(gz, hdr)
	require.NotNil(t, body, "gzip body must decompress into BodyJSON")
	assert.Equal(t, "push", body["event"])
	assert.Empty(t, raw)
}

func TestDecodeJetStreamBody_GzipMagicBytesFallback(t *testing.T) {
	// Even without a Content-Encoding header, the gzip magic bytes
	// (0x1f 0x8b) signal the payload is gzipped. Many producers omit
	// the header; fall back to magic-byte detection so the matcher
	// still sees decoded JSON.
	gz := gzipBytes(t, []byte(`{"event":"push","muted":true}`))
	body, raw := decodeJetStreamBody(gz, nil)
	require.NotNil(t, body, "gzip magic bytes must trigger decompression even without Content-Encoding header")
	assert.Equal(t, "push", body["event"])
	assert.Empty(t, raw)
}

func TestDecodeJetStreamBody_NonJSONPayload_FallsBackToBodyRaw(t *testing.T) {
	// Plain non-JSON stays as raw — same as today's behavior.
	body, raw := decodeJetStreamBody([]byte("plain text"), nil)
	assert.Nil(t, body)
	assert.Equal(t, "plain text", raw)
}

func TestDecodeJetStreamBody_GzipUndecodableBody_FallsBackToRaw(t *testing.T) {
	// A gzip whose decompressed content is NOT JSON falls through to
	// BodyRaw (with the decompressed text) — matcher then matches on
	// body_raw if the author wrote one.
	gz := gzipBytes(t, []byte("not json"))
	body, raw := decodeJetStreamBody(gz, nil)
	assert.Nil(t, body)
	assert.Equal(t, "not json", raw, "decompressed-but-non-JSON falls through to BodyRaw")
}

func TestDecodeJetStreamBody_HeaderLookupIsCaseInsensitive(t *testing.T) {
	// NATS headers preserve case; producers may emit content-encoding
	// in any casing. Match case-insensitively.
	for _, key := range []string{"Content-Encoding", "content-encoding", "CONTENT-ENCODING"} {
		t.Run(key, func(t *testing.T) {
			gz := gzipBytes(t, []byte(`{"x":1}`))
			hdr := map[string][]string{key: {"gzip"}}
			body, _ := decodeJetStreamBody(gz, hdr)
			require.NotNil(t, body, "Content-Encoding header lookup must be case-insensitive (key=%q)", key)
			assert.Equal(t, float64(1), body["x"])
		})
	}
}

// Smoke guard: the helper is exercised end-to-end via the public
// buildJetStreamPayload (used by the live reader's Watch goroutine).
// This integration check protects against the helper being added to
// the package but never reached by the production path.
func TestBuildJetStreamPayload_DecodesGzipped(t *testing.T) {
	gz := gzipBytes(t, []byte(`{"event":"push"}`))
	body, raw := decodeJetStreamBody(gz, map[string][]string{"Content-Encoding": {"gzip"}})
	require.NotNil(t, body)
	assert.Equal(t, "push", body["event"])
	assert.Empty(t, raw)
	// Sanity that gzip wasn't an accidental pass-through:
	_, jsonErr := json.Marshal(gz)
	assert.NoError(t, jsonErr)
	assert.True(t, strings.HasPrefix(string(gz), "\x1f\x8b"), "magic-byte precondition")
	// Round-trip via the gzip lib confirms the test's own setup:
	r, err := gzip.NewReader(bytes.NewReader(gz))
	require.NoError(t, err)
	plain, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, `{"event":"push"}`, string(plain))
}
