package readers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// JetStreamSubjectReader observes a JetStream subject pattern via an
// ephemeral, deliver-new, filtered consumer scoped to the scenario's
// timeframe. Captures every message published during the timeframe,
// decodes the payload (best-effort JSON), and emits one Event per
// message. The ephemeral consumer is deleted at ctx-cancel.
//
// This reader does NOT classify or interpret — matchers do that.
//
// The reader is created per-stream + subject-filter (e.g. one instance
// for the rooms-canonical stream with filter
// `chat.room.canonical.*.>`). Wiring details live with the runner.
type JetStreamSubjectReader struct {
	conn          *nats.Conn
	domain        string // JS domain (site name); empty = default domain
	streamName    string
	subjectFilter string
	location      string // catalog location name, set on emitted events
	ownerSvc      string // owner attribution for the classifier
}

// NewJetStreamSubjectReader returns a reader bound to the given
// JetStream stream + subject filter. The conn must already be
// connected; the reader does not own its lifecycle.
func NewJetStreamSubjectReader(conn *nats.Conn, streamName, subjectFilter, location, ownerSvc string) *JetStreamSubjectReader {
	return &JetStreamSubjectReader{
		conn:          conn,
		streamName:    streamName,
		subjectFilter: subjectFilter,
		location:      location,
		ownerSvc:      ownerSvc,
	}
}

// NewJetStreamSubjectReaderWithDomain returns a reader that opens
// the JetStream context via jetstream.NewWithDomain(conn, domain).
// Used by the site-aware jetstream_consume poller (Task 19).
func NewJetStreamSubjectReaderWithDomain(conn *nats.Conn, domain, streamName, subjectFilter, location, ownerSvc string) *JetStreamSubjectReader {
	return &JetStreamSubjectReader{
		conn:          conn,
		domain:        domain,
		streamName:    streamName,
		subjectFilter: subjectFilter,
		location:      location,
		ownerSvc:      ownerSvc,
	}
}

// JetStreamSubjectPayload is the typed Event.Payload emitted by
// JetStreamSubjectReader. Mirrors the capture-and-emit discipline of
// ReplyPayload — the reader records every observable fact and lets
// matchers interpret.
type JetStreamSubjectPayload struct {
	Subject  string              `json:"subject"`
	BodyJSON map[string]any      `json:"body_json,omitempty"`
	BodyRaw  string              `json:"body_raw,omitempty"`
	Header   map[string][]string `json:"header,omitempty"`
	Sequence uint64              `json:"sequence"`
}

// Watch starts an ephemeral JetStream consumer at deliver-by-start-time
// scoped to ctx + start. start binds the consumer's OptStartTime so a
// lazily-opened consumer (Phase 4.0 jetstream_consume primitive opens
// on first PollFn call, *after* the verb has fired) replays every
// message published since the sandbox session boundary. Without this,
// the timeline race between "case fires" and "consumer becomes ready"
// would silently drop the target event under DeliverNewPolicy.
//
// Cleanup deletes the consumer on ctx-done; InactiveThreshold is a
// safety net so an orphaned consumer dies on its own.
func (r *JetStreamSubjectReader) Watch(ctx context.Context, _ string, start time.Time) (<-chan Event, error) {
	var (
		js  jetstream.JetStream
		err error
	)
	if r.domain != "" {
		js, err = jetstream.NewWithDomain(r.conn, r.domain)
	} else {
		js, err = jetstream.New(r.conn)
	}
	if err != nil {
		return nil, fmt.Errorf("jetstream consumer: connect: %w", err)
	}

	stream, err := js.Stream(ctx, r.streamName)
	if err != nil {
		return nil, fmt.Errorf("jetstream consumer: stream %q: %w", r.streamName, err)
	}

	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		FilterSubject:     r.subjectFilter,
		DeliverPolicy:     jetstream.DeliverByStartTimePolicy,
		OptStartTime:      &start,
		AckPolicy:         jetstream.AckNonePolicy,
		InactiveThreshold: 30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("jetstream consumer: create consumer: %w", err)
	}

	out := make(chan Event, 16)
	var dropped atomic.Uint64
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		ev := Event{
			Location:  r.location,
			Timestamp: msgTime(msg),
			OwnerSvc:  r.ownerSvc,
			Payload:   buildJetStreamPayload(msg),
			Type:      EventCascade,
		}
		if hdr := msg.Headers(); hdr != nil {
			ev.Traceparent = hdr.Get("traceparent")
		}
		select {
		case out <- ev:
		default:
			// Observer buffer full — drop is preferable to blocking the
			// JetStream consumer goroutine (matches NATSReplyReader
			// dropping policy). A drop is a substrate-level signal
			// loss: the message DID arrive, but the matcher will not
			// see it. Warn on first drop and again every 100 so a
			// chronically-undersized buffer surfaces in the log
			// without flooding it; matters for §2.9 because a `not:
			// true` assertion could falsely-green if the dropped
			// message was the one the author wanted us to observe.
			n := dropped.Add(1)
			if n == 1 || n%100 == 0 {
				slog.Warn("jetstream consumer: observer buffer full — message DROPPED before matcher could see it; counts (dropped, channel cap) — investigate buffer sizing or polling cadence",
					"stream", r.streamName, "filter_subject", r.subjectFilter,
					"location", r.location, "dropped_so_far", n, "buffer_cap", 16,
					"subject", msg.Subject())
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("jetstream consumer: consume: %w", err)
	}

	go func() {
		<-ctx.Done()
		cc.Stop()
		// Best-effort cleanup; consumer also self-expires via
		// InactiveThreshold. Surface a delete failure as a warn so a
		// leaked consumer (which would survive the 30s threshold and
		// then take itself out, but accrue server-side state in the
		// meantime) doesn't go unnoticed. Suppress the "consumer not
		// found" case — likely the InactiveThreshold reaped it before
		// we got here.
		delName := cons.CachedInfo().Name
		if delErr := stream.DeleteConsumer(context.Background(), delName); delErr != nil && !errors.Is(delErr, jetstream.ErrConsumerNotFound) {
			slog.Warn("jetstream consumer: teardown DeleteConsumer failed — consumer may persist until its 30s InactiveThreshold expires",
				"stream", r.streamName, "consumer", delName, "err", delErr)
		}
		if total := dropped.Load(); total > 0 {
			slog.Warn("jetstream consumer: lifetime drop count on teardown — these messages were observed by the JS consumer but DROPPED before the matcher could see them",
				"stream", r.streamName, "filter_subject", r.subjectFilter,
				"location", r.location, "dropped_total", total)
		}
		close(out)
	}()

	return out, nil
}

func msgTime(msg jetstream.Msg) time.Time {
	meta, err := msg.Metadata()
	if err == nil && !meta.Timestamp.IsZero() {
		return meta.Timestamp
	}
	return time.Now()
}

func buildJetStreamPayload(msg jetstream.Msg) JetStreamSubjectPayload {
	p := JetStreamSubjectPayload{Subject: msg.Subject()}
	if meta, err := msg.Metadata(); err == nil {
		p.Sequence = meta.Sequence.Stream
	}
	data := msg.Data()
	if len(data) > 0 {
		// Headers feed Content-Encoding detection. Copy them into a
		// plain map[string][]string for both the payload and the
		// helper — nats.Header alias is map[string][]string under the
		// hood, so this is just a flatten + decouple.
		var hdr map[string][]string
		if h := msg.Headers(); len(h) > 0 {
			hdr = make(map[string][]string, len(h))
			for k, v := range h {
				out := make([]string, len(v))
				copy(out, v)
				hdr[k] = out
			}
		}
		p.BodyJSON, p.BodyRaw = decodeJetStreamBody(data, hdr)
		// Header capture for the matcher mirrors what we already
		// gathered above. Stash it unconditionally when present so
		// authors can assert on headers regardless of body shape.
		if len(hdr) > 0 {
			p.Header = hdr
		}
	} else if hdr := msg.Headers(); len(hdr) > 0 {
		cp := make(map[string][]string, len(hdr))
		for k, v := range hdr {
			out := make([]string, len(v))
			copy(out, v)
			cp[k] = out
		}
		p.Header = cp
	}
	return p
}

// decodeJetStreamBody normalises a JetStream message payload into
// (BodyJSON, BodyRaw). Gzip-compressed bodies are detected by either:
//   - Content-Encoding: gzip header (case-insensitive), OR
//   - gzip magic bytes (0x1f 0x8b) at the start of the payload.
//
// Detection is tolerant of producers that omit the header (push-
// notification fan-out is the motivating consumer — F-016/F-018 push
// behavior was previously code-bounded only).
//
// Decompression failures fall through to the raw bytes so the matcher
// can still assert on body_raw or surface the diagnostic.
func decodeJetStreamBody(data []byte, hdr map[string][]string) (bodyJSON map[string]any, bodyRaw string) {
	payload := data
	if isGzipped(payload, hdr) {
		if decoded, err := gunzip(payload); err == nil {
			payload = decoded
		}
	}
	if len(payload) == 0 {
		return nil, ""
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err == nil {
		return m, ""
	}
	return nil, string(payload)
}

// isGzipped reports whether data looks gzipped — by header OR by the
// gzip magic bytes (0x1f 0x8b). Header lookup is case-insensitive.
func isGzipped(data []byte, hdr map[string][]string) bool {
	for k, vs := range hdr {
		if strings.EqualFold(k, "Content-Encoding") {
			for _, v := range vs {
				if strings.EqualFold(strings.TrimSpace(v), "gzip") {
					return true
				}
			}
		}
	}
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

func gunzip(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
