package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// HeaderRunID is the NATS header key stamped on every publish/request
// so SUT services can correlate their traces / logs / metrics back to
// the loadgen run that drove them. C3 fix.
const HeaderRunID = "X-Loadgen-Run-ID"

type natsCorePublisher struct {
	pool         *ConnPool // Phase 3 §3.6 — picks the data conn per subject's userID
	useJetStream bool
	js           jetstream.JetStream
	runID        string // C3: stamped as an X-Loadgen-Run-ID header on every publish
	// S5: when true, canonical-injection publishes use js.PublishMsgAsync
	// (returns immediately; ack tracked via WithPublishAsyncErrHandler set
	// on the JetStream client). When false, fall back to the legacy sync
	// js.PublishMsg path so operators can bisect throughput regressions.
	asyncJS bool
}

func newNatsCorePublisher(pool *ConnPool, inject InjectMode, js jetstream.JetStream) *natsCorePublisher {
	return &natsCorePublisher{pool: pool, useJetStream: inject == InjectCanonical, js: js}
}

// newWarmupPublisher returns a publisher dedicated to the auto-warmup
// phase. Bug 3: auto-warmup always publishes to frontdoor subjects
// (chat.user.{account}.room.…msg.send) which are NOT in any JetStream
// stream, so the run-level publisher's useJetStream / asyncJS flags
// MUST be ignored or PublishMsgAsync will silently drop the warmup
// publishes and leave the message-ID pool empty. We reuse the same
// conn pool + runID for trace correlation.
func newWarmupPublisher(run *natsCorePublisher) *natsCorePublisher {
	return &natsCorePublisher{
		pool:         run.pool,
		useJetStream: false,
		asyncJS:      false,
		js:           nil,
		runID:        run.runID,
	}
}

func (p *natsCorePublisher) Publish(ctx context.Context, subject string, data []byte) error {
	if p.useJetStream {
		// JetStream publishes go through one writer; canonical-injection
		// is a single-stream concern, not a per-user concern.
		msg := &nats.Msg{Subject: subject, Data: data}
		if p.runID != "" {
			msg.Header = nats.Header{HeaderRunID: []string{p.runID}}
		}
		if p.asyncJS {
			// S5: PublishMsgAsync returns immediately; the per-publish
			// PubAck is processed off-loop. Errors land in the
			// WithPublishAsyncErrHandler set at jetstream.New time, which
			// updates loadgen_publish_errors_total{reason="async_ack"}.
			// When the in-flight window is full (configured by
			// WithPublishAsyncMaxPending), this call blocks — that's the
			// designed backpressure path.
			if _, err := p.js.PublishMsgAsync(msg); err != nil {
				return fmt.Errorf("jetstream publish async: %w", err)
			}
			return nil
		}
		if _, err := p.js.PublishMsg(ctx, msg); err != nil {
			return fmt.Errorf("jetstream publish: %w", err)
		}
		return nil
	}
	conn := p.pool.For(UserFromSubject(subject))
	msg := &nats.Msg{Subject: subject, Data: data}
	if p.runID != "" {
		msg.Header = nats.Header{HeaderRunID: []string{p.runID}}
	}
	if err := conn.PublishMsg(msg); err != nil {
		return fmt.Errorf("core publish: %w", err)
	}
	return nil
}

// newAsyncErrHandler returns the WithPublishAsyncErrHandler closure used
// by the S5 async publish path. Split out from jetstreamPublishOpts for
// direct testability — the closure is the entire correctness story for
// async error reporting and orphan eviction.
//
// On per-publish failure (NoResponders, stream not found, max-pending
// stall, etc.), the handler:
//
//   - bumps loadgen_publish_errors_total{preset, reason="async_ack"}; the
//     preset is captured by closure so cardinality stays bounded by the
//     {preset, reason} pair already used elsewhere; presetName binding
//     is one-shot, tied to the run's preset (R1 NIT #12).
//   - decodes msg.Data as a model.MessageEvent (canonical schema is the
//     only payload that reaches this path) and calls
//     collector.RecordPublishFailed("", messageID). That evicts the
//     orphaned messageID from byMsgID + seenMessageIDs so it doesn't
//     inflate Finalize's MissingBroadcasts and doesn't get handed to
//     the auto-warmup MessageIDs() pool. The empty requestID is a no-op
//     under RecordPublishFailed (delete-from-map of empty key is a
//     no-op).
//
// JSON unmarshal in the hot path is acceptable because the handler only
// fires on errors (rare) — happy-path publishes never reach here.
func newAsyncErrHandler(m *Metrics, presetName string, collector *Collector) func(jetstream.JetStream, *nats.Msg, error) {
	return func(_ jetstream.JetStream, msg *nats.Msg, _ error) {
		m.PublishErrors.WithLabelValues(presetName, "async_ack").Inc()
		if collector == nil || msg == nil || len(msg.Data) == 0 {
			return
		}
		var stub struct {
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
		}
		if err := json.Unmarshal(msg.Data, &stub); err != nil || stub.Message.ID == "" {
			return
		}
		collector.RecordPublishFailed("", stub.Message.ID)
	}
}

// jetstreamPublishOpts returns the JetStream client options that enable
// the S5 async publish path. When maxPending<=0 the async path is
// disabled and the helper returns nil so jetstream.New keeps its
// defaults (sync publishes via PublishMsg).
func jetstreamPublishOpts(maxPending int, m *Metrics, presetName string, collector *Collector) []jetstream.JetStreamOpt {
	if maxPending <= 0 {
		return nil
	}
	return []jetstream.JetStreamOpt{
		jetstream.WithPublishAsyncMaxPending(maxPending),
		jetstream.WithPublishAsyncErrHandler(newAsyncErrHandler(m, presetName, collector)),
	}
}
