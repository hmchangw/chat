package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const (
	defaultInitialBackoff = 200 * time.Millisecond
	defaultMaxBackoff     = 30 * time.Second
)

// publisher is the minimal JetStream publish surface the watcher needs.
// oteljetstream.JetStream satisfies it (PubAck is a jetstream.PubAck alias).
type publisher interface {
	PublishMsg(ctx context.Context, msg *nats.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

// changeSource yields decoded change events in oplog order. Next blocks until
// the next event is available; it returns context.Canceled (wrapped) when the
// stream is stopped, which the watcher treats as a graceful stop.
type changeSource interface {
	Next(ctx context.Context) (changeEvent, error)
	Close(ctx context.Context) error
}

// watcher runs the per-collection pipeline: read one change event, publish it
// synchronously (blocking on the pub-ack), then advance the checkpoint. Per
// collection there is exactly one watcher on one connection, so publish order
// = stream-sequence order, and the checkpoint never advances past an
// un-acked event (lossless; duplicates collapse on Nats-Msg-Id dedup).
type watcher struct {
	siteID     string
	collection string
	source     changeSource
	pub        publisher
	store      CheckpointStore

	checkpointEvery int
	initialBackoff  time.Duration
	maxBackoff      time.Duration
	now             func() int64 // unix ms; injectable for tests
	log             *slog.Logger
}

func newWatcher(siteID, collection string, src changeSource, pub publisher, store CheckpointStore, checkpointEvery int) *watcher {
	return &watcher{
		siteID:          siteID,
		collection:      collection,
		source:          src,
		pub:             pub,
		store:           store,
		checkpointEvery: checkpointEvery,
		initialBackoff:  defaultInitialBackoff,
		maxBackoff:      defaultMaxBackoff,
		now:             func() int64 { return time.Now().UTC().UnixMilli() },
		log:             slog.With("collection", collection),
	}
}

// run drives the watcher until the context is cancelled (graceful — returns
// nil after persisting the final checkpoint) or a fatal error occurs (returns
// non-nil; the caller exits non-zero). A lost resume token (Mongo code 286) is
// fatal by design: silently reseeding-from-now would drop events.
func (w *watcher) run(ctx context.Context) error {
	defer func() {
		// Best-effort close, detached from the (possibly cancelled) ctx.
		_ = w.source.Close(context.WithoutCancel(ctx))
	}()

	var last changeEvent
	haveLast := false
	sinceSave := 0

	for {
		ev, err := w.source.Next(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// Graceful stop — persist the final frontier so restart resumes
				// exactly after the last published event.
				if haveLast {
					if serr := w.saveCheckpoint(context.WithoutCancel(ctx), &last); serr != nil {
						w.log.Error("final checkpoint save failed", "error", serr)
					}
				}
				return nil
			}
			if isHistoryLost(err) {
				return fmt.Errorf("resume token lost for %q — operator reseed required (history lost): %w", w.collection, err)
			}
			return fmt.Errorf("read change stream %q: %w", w.collection, err)
		}

		if err := w.publishWithRetry(ctx, &ev); err != nil {
			// Only ctx cancellation breaks the retry loop; treat as graceful.
			if haveLast {
				if serr := w.saveCheckpoint(context.WithoutCancel(ctx), &last); serr != nil {
					w.log.Error("final checkpoint save failed", "error", serr)
				}
			}
			return nil
		}

		last = ev
		haveLast = true
		sinceSave++
		if sinceSave >= w.checkpointEvery {
			if err := w.saveCheckpoint(ctx, &ev); err != nil {
				// Non-fatal: a failed checkpoint only means more replay on crash
				// (deduped), never loss. Keep going and retry next interval.
				w.log.Error("checkpoint save failed", "eventId", ev.EventID, "error", err)
			} else {
				sinceSave = 0
			}
		}
	}
}

// publishWithRetry publishes one event synchronously, retrying with capped
// exponential backoff until the pub-ack succeeds or the context is cancelled.
// The checkpoint never advances until this returns nil.
func (w *watcher) publishWithRetry(ctx context.Context, ev *changeEvent) error {
	subj, msgID, evt, err := buildEnvelope(ev, w.siteID, w.now())
	if err != nil {
		// A malformed BSON document is a poison event; log and skip rather than
		// wedge the whole collection. This is the only event we drop.
		w.log.Error("build envelope failed — skipping event", "eventId", ev.EventID, "error", err)
		return nil
	}
	data, err := json.Marshal(evt)
	if err != nil {
		w.log.Error("marshal oplog event failed — skipping event", "eventId", ev.EventID, "error", err)
		return nil
	}

	msg := &nats.Msg{Subject: subj, Data: data, Header: nats.Header{}}
	msg.Header.Set("Nats-Msg-Id", msgID)

	backoff := w.initialBackoff
	for {
		if _, err := w.pub.PublishMsg(ctx, msg); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else {
			w.log.Error("publish failed — retrying", "eventId", msgID, "backoff", backoff.String(), "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < w.maxBackoff {
			backoff *= 2
			if backoff > w.maxBackoff {
				backoff = w.maxBackoff
			}
		}
	}
}

func (w *watcher) saveCheckpoint(ctx context.Context, ev *changeEvent) error {
	return w.store.Save(ctx, &Checkpoint{
		SiteID:      w.siteID,
		Collection:  w.collection,
		ResumeToken: ev.ResumeToken,
		ClusterTime: ev.ClusterTimeMs,
		EventID:     ev.EventID,
		Source:      "runtime",
		UpdatedAt:   w.now(),
	})
}

// isHistoryLost reports whether err is a Mongo ChangeStreamHistoryLost (286),
// meaning the resume token is no longer in the oplog and a reseed is required.
func isHistoryLost(err error) bool {
	var se mongo.ServerError
	if errors.As(err, &se) {
		return se.HasErrorCode(286)
	}
	return false
}
