package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/searchengine"
)

// pendingMsg tracks a JetStream message and the range of bulk actions it
// produced. A single JetStream message may fan out into zero, one, or multiple
// actions. The message is acked once ALL of its actions succeed; if any action
// fails the whole message is nakked for redelivery.
type pendingMsg struct {
	jsMsg       jetstream.Msg
	actionStart int // starting index into Handler.actions
	actionCount int // number of actions contributed by this message
}

// Handler buffers JetStream messages and the ES bulk actions they produce,
// then flushes the actions as a single ES bulk request.
//
// Two counts are tracked separately because they can diverge for fan-out
// collections (one JetStream message producing N ES actions):
//
//   - MessageCount() reports buffered source messages. Used for per-source
//     ack/nak accounting at flush time.
//   - ActionCount() reports buffered ES bulk actions. This is what bounds
//     the size of the next ES bulk request and should drive the flush
//     decision in the consumer loop.
//
// For 1:1 collections (messages, and the single-subscription path of
// spotlight/user-room) MessageCount() == ActionCount(). For fan-out
// collections (bulk-invite spotlight/user-room) ActionCount() >=
// MessageCount().
type Handler struct {
	store      Store
	collection Collection
	bulkSize   int // soft cap on buffered actions; callers drive flush via ActionCount()
	mu         sync.Mutex
	pending    []pendingMsg
	actions    []searchengine.BulkAction
}

// NewHandler creates a Handler with the given store, collection, and bulk
// batch size. `bulkSize` is the soft cap on buffered actions before a flush
// is triggered — the consumer loop compares it against `ActionCount()` to
// decide when to call `Flush`.
func NewHandler(store Store, collection Collection, bulkSize int) *Handler {
	return &Handler{
		store:      store,
		collection: collection,
		bulkSize:   bulkSize,
		pending:    make([]pendingMsg, 0, bulkSize),
		actions:    make([]searchengine.BulkAction, 0, bulkSize),
	}
}

// Add parses a JetStream message via the collection and adds its actions to
// the buffer. If the collection produces zero actions (e.g., a filtered
// event), the message is immediately acked without touching the buffer.
func (h *Handler) Add(msg jetstream.Msg) {
	actions, err := h.collection.BuildAction(msg.Data())
	if err != nil {
		slog.Error("build action", "error", err)
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("ack malformed message", "error", ackErr)
		}
		return
	}

	if len(actions) == 0 {
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("ack filtered message", "error", ackErr)
		}
		return
	}

	h.mu.Lock()
	h.pending = append(h.pending, pendingMsg{
		jsMsg:       msg,
		actionStart: len(h.actions),
		actionCount: len(actions),
	})
	h.actions = append(h.actions, actions...)
	h.mu.Unlock()
}

// Flush sends all buffered actions to ES and acks/naks per source message.
func (h *Handler) Flush(ctx context.Context) {
	h.mu.Lock()
	if len(h.pending) == 0 {
		h.mu.Unlock()
		return
	}
	pending := h.pending
	actions := h.actions
	h.pending = make([]pendingMsg, 0, h.bulkSize)
	h.actions = make([]searchengine.BulkAction, 0, h.bulkSize)
	h.mu.Unlock()

	results, err := h.store.Bulk(ctx, actions)
	if err != nil {
		slog.Error("bulk request failed", "error", err, "actions", len(actions))
		nakAll(pending)
		return
	}

	if len(results) != len(actions) {
		// Defensive guard for a protocol-level anomaly: ES bulk API normally
		// returns one result per input action in input order. Nak-all is safe
		// because every action type we emit is idempotent on redelivery:
		//   - ActionIndex / ActionDelete: external versioning makes a stale
		//     redelivery return 409 (handled as ack below); a successful
		//     redelivery is identical to the original write.
		//   - ActionUpdate: the painless scripts in user_room.go check a
		//     per-room timestamp guard (params.ts > stored) and short-circuit
		//     via ctx.op = 'none' on a redelivery, so a redelivered update
		//     is at worst a no-op.
		// No duplicate processing, no lost events.
		slog.Error("bulk result count mismatch", "expected", len(actions), "actual", len(results))
		nakAll(pending)
		return
	}

	for _, p := range pending {
		allOK := true
		for i := p.actionStart; i < p.actionStart+p.actionCount; i++ {
			if isBulkItemSuccess(actions[i].Action, results[i]) {
				continue
			}
			allOK = false
			slog.Error("bulk item failed",
				"status", results[i].Status,
				"error", results[i].Error,
				"docID", actions[i].DocID,
				"index", actions[i].Index,
			)
			break
		}
		if allOK {
			if ackErr := p.jsMsg.Ack(); ackErr != nil {
				slog.Error("ack failed", "error", ackErr)
			}
		} else {
			if nakErr := p.jsMsg.Nak(); nakErr != nil {
				slog.Error("nak failed", "error", nakErr)
			}
		}
	}
}

// isBulkItemSuccess maps an ES bulk item result to a logical success/failure
// per action type.
//
//   - 2xx is always success.
//   - 409 is success on every action: it means external versioning rejected
//     a stale write, and the desired state is already reached or newer.
//   - 404 is success on ActionDelete (already deleted) and on ActionUpdate
//     (the user-room remove path emits a scriptless update on a doc that
//     may not exist yet — desired state already reached). It remains a
//     failure on ActionIndex because indexing is supposed to create the
//     doc.
func isBulkItemSuccess(action searchengine.ActionType, result searchengine.BulkResult) bool {
	if result.Status >= 200 && result.Status < 300 {
		return true
	}
	if result.Status == 409 {
		return true
	}
	if result.Status == 404 && (action == searchengine.ActionDelete || action == searchengine.ActionUpdate) {
		return true
	}
	return false
}

func nakAll(pending []pendingMsg) {
	for _, p := range pending {
		if nakErr := p.jsMsg.Nak(); nakErr != nil {
			slog.Error("nak failed", "error", nakErr)
		}
	}
}

// MessageCount returns the number of buffered source JetStream messages.
// This is used for diagnostics and for the per-source ack/nak accounting at
// flush time; it is NOT the quantity that should drive the flush decision
// for fan-out collections — use ActionCount() for that.
func (h *Handler) MessageCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending)
}

// ActionCount returns the number of buffered ES bulk actions. For 1:1
// collections this equals MessageCount(); for fan-out collections (bulk
// invites producing N actions per event) it is ≥ MessageCount(). The
// consumer loop compares this against the configured bulk batch size to
// decide when to flush so ES bulk requests stay bounded regardless of
// fan-out.
func (h *Handler) ActionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.actions)
}
