package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/searchengine"
)

type bufferedMsg struct {
	action searchengine.BulkAction
	jsMsg  jetstream.Msg
}

// Handler buffers JetStream messages and flushes them as ES bulk requests.
type Handler struct {
	store      Store
	collection Collection
	batchSize  int
	mu         sync.Mutex
	buffer     []bufferedMsg
}

// NewHandler creates a Handler with the given store, collection, and batch size.
func NewHandler(store Store, collection Collection, batchSize int) *Handler {
	return &Handler{
		store:      store,
		collection: collection,
		batchSize:  batchSize,
		buffer:     make([]bufferedMsg, 0, batchSize),
	}
}

// Add parses a JetStream message via the collection and adds it to the buffer.
func (h *Handler) Add(msg jetstream.Msg) {
	action, err := h.collection.BuildAction(msg.Data())
	if err != nil {
		slog.Error("build action", "error", err)
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("ack malformed message", "error", ackErr)
		}
		return
	}

	h.mu.Lock()
	h.buffer = append(h.buffer, bufferedMsg{action: action, jsMsg: msg})
	h.mu.Unlock()
}

// Flush sends all buffered actions to ES and acks/naks per item.
func (h *Handler) Flush(ctx context.Context) {
	h.mu.Lock()
	if len(h.buffer) == 0 {
		h.mu.Unlock()
		return
	}
	items := h.buffer
	h.buffer = make([]bufferedMsg, 0, h.batchSize)
	h.mu.Unlock()

	actions := make([]searchengine.BulkAction, len(items))
	for i, item := range items {
		actions[i] = item.action
	}

	results, err := h.store.Bulk(ctx, actions)
	if err != nil {
		slog.Error("bulk request failed", "error", err, "count", len(items))
		for _, item := range items {
			if nakErr := item.jsMsg.Nak(); nakErr != nil {
				slog.Error("nak failed", "error", nakErr)
			}
		}
		return
	}

	for i, result := range results {
		if result.Status == 409 || (result.Status >= 200 && result.Status < 300) {
			if ackErr := items[i].jsMsg.Ack(); ackErr != nil {
				slog.Error("ack failed", "error", ackErr)
			}
		} else {
			slog.Error("bulk item failed", "status", result.Status, "error", result.Error, "docID", items[i].action.DocID)
			if nakErr := items[i].jsMsg.Nak(); nakErr != nil {
				slog.Error("nak failed", "error", nakErr)
			}
		}
	}
}

// BufferLen returns the current buffer size.
func (h *Handler) BufferLen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.buffer)
}

// BufferFull returns true if the buffer has reached batch size.
func (h *Handler) BufferFull() bool {
	return h.BufferLen() >= h.batchSize
}
