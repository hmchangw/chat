package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/searchengine"
)

type pendingMsg struct {
	jsMsg       jetstream.Msg
	actionStart int
	actionCount int
}

type Handler struct {
	store      Store
	collection Collection
	bulkSize   int
	mu         sync.Mutex
	pending    []pendingMsg
	actions    []searchengine.BulkAction
}

func NewHandler(store Store, collection Collection, bulkSize int) *Handler {
	return &Handler{
		store:      store,
		collection: collection,
		bulkSize:   bulkSize,
		pending:    make([]pendingMsg, 0, bulkSize),
		actions:    make([]searchengine.BulkAction, 0, bulkSize),
	}
}

func (h *Handler) Add(msg jetstream.Msg) {
	actions, err := h.collection.BuildAction(msg.Data())
	if err != nil {
		slog.Error("build action", "error", err)
		natsutil.Ack(msg, "build action failed")
		return
	}

	if len(actions) == 0 {
		natsutil.Ack(msg, "filtered, no actions")
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
		nakAll(pending, "bulk request failed")
		return
	}

	if len(results) != len(actions) {
		slog.Error("bulk result count mismatch", "expected", len(actions), "actual", len(results))
		nakAll(pending, "bulk result count mismatch")
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
			natsutil.Ack(p.jsMsg, "bulk actions succeeded")
		} else {
			natsutil.Nak(p.jsMsg, "bulk action failed")
		}
	}
}

const (
	esErrDocumentMissing = "document_missing_exception"
	esErrIndexNotFound   = "index_not_found_exception"
)

func isBulkItemSuccess(action searchengine.ActionType, result searchengine.BulkResult) bool {
	if result.Status >= 200 && result.Status < 300 {
		return true
	}
	if result.Status == 409 {
		// External versioning rejected a stale write. Success for ActionIndex
		// and ActionDelete (desired state already reached). For ActionUpdate,
		// 409 means ES internal OCC conflict (seq_no mismatch) — the painless
		// script didn't run, so we need a retry via JetStream redelivery.
		return action != searchengine.ActionUpdate
	}
	if result.Status == 404 {
		switch action {
		case searchengine.ActionDelete:
			return result.ErrorType == ""
		case searchengine.ActionUpdate:
			return result.ErrorType == esErrDocumentMissing
		case searchengine.ActionIndex:
			return false
		}
	}
	return false
}

func nakAll(pending []pendingMsg, reason string) {
	for _, p := range pending {
		natsutil.Nak(p.jsMsg, reason)
	}
}

func MessageCount(h *Handler) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending)
}

func (h *Handler) ActionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.actions)
}
