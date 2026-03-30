package main

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

type natsSub struct {
	id      string
	subject string
	sub     *nats.Subscription
}

type natsHub struct {
	mu         sync.RWMutex
	sourceConn *nats.Conn
	destConn   *nats.Conn
	sourceURL  string
	destURL    string
	subs       map[string]*natsSub
	clients    map[string]chan<- Message
}

func newNATSHub() *natsHub {
	return &natsHub{
		subs:    make(map[string]*natsSub),
		clients: make(map[string]chan<- Message),
	}
}

func (h *natsHub) Connect(sourceURL, destURL string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.disconnectLocked()

	srcConn, err := nats.Connect(sourceURL,
		nats.Name("nats-debug-source"),
		nats.MaxReconnects(0),
	)
	if err != nil {
		return fmt.Errorf("connect to source NATS %s: %w", sourceURL, err)
	}

	dstConn, err := nats.Connect(destURL,
		nats.Name("nats-debug-dest"),
		nats.MaxReconnects(0),
	)
	if err != nil {
		srcConn.Close()
		return fmt.Errorf("connect to dest NATS %s: %w", destURL, err)
	}

	h.sourceConn = srcConn
	h.destConn = dstConn
	h.sourceURL = sourceURL
	h.destURL = destURL

	slog.Info("connected to NATS servers", "source", sourceURL, "dest", destURL)
	return nil
}

func (h *natsHub) Disconnect() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disconnectLocked()
}

// disconnectLocked closes connections and clears state. Caller must hold mu.
func (h *natsHub) disconnectLocked() {
	for id, ns := range h.subs {
		if err := ns.sub.Unsubscribe(); err != nil {
			slog.Warn("unsubscribe on disconnect", "id", id, "error", err)
		}
	}
	h.subs = make(map[string]*natsSub)

	if h.sourceConn != nil {
		h.sourceConn.Close()
		h.sourceConn = nil
	}
	if h.destConn != nil {
		h.destConn.Close()
		h.destConn = nil
	}
	h.sourceURL = ""
	h.destURL = ""
}

func (h *natsHub) Subscribe(subject string) (Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.destConn == nil {
		return Subscription{}, fmt.Errorf("not connected to dest NATS")
	}

	id := uuid.NewString()
	sub, err := h.destConn.Subscribe(subject, func(msg *nats.Msg) {
		h.broadcast(Message{
			ID:        uuid.NewString(),
			Subject:   msg.Subject,
			Payload:   string(msg.Data),
			Timestamp: time.Now().UTC(),
		})
	})
	if err != nil {
		return Subscription{}, fmt.Errorf("subscribe to %s: %w", subject, err)
	}

	h.subs[id] = &natsSub{id: id, subject: subject, sub: sub}
	slog.Info("subscribed", "id", id, "subject", subject)
	return Subscription{ID: id, Subject: subject}, nil
}

func (h *natsHub) Unsubscribe(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	ns, ok := h.subs[id]
	if !ok {
		return fmt.Errorf("subscription %s not found", id)
	}
	if err := ns.sub.Unsubscribe(); err != nil {
		return fmt.Errorf("unsubscribe %s: %w", id, err)
	}
	delete(h.subs, id)
	slog.Info("unsubscribed", "id", id, "subject", ns.subject)
	return nil
}

func (h *natsHub) Publish(subject, payload string) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.sourceConn == nil {
		return fmt.Errorf("not connected to source NATS")
	}
	if err := h.sourceConn.Publish(subject, []byte(payload)); err != nil {
		return fmt.Errorf("publish to %s: %w", subject, err)
	}
	slog.Info("published", "subject", subject)
	return nil
}

func (h *natsHub) Status() ConnectionStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return ConnectionStatus{
		SourceConnected: h.sourceConn != nil && h.sourceConn.IsConnected(),
		DestConnected:   h.destConn != nil && h.destConn.IsConnected(),
		SourceURL:       h.sourceURL,
		DestURL:         h.destURL,
	}
}

func (h *natsHub) Subscriptions() []Subscription {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Subscription, 0, len(h.subs))
	for _, ns := range h.subs {
		out = append(out, Subscription{ID: ns.id, Subject: ns.subject})
	}
	return out
}

func (h *natsHub) RegisterSSEClient(ch chan<- Message) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := uuid.NewString()
	h.clients[id] = ch
	return id
}

func (h *natsHub) UnregisterSSEClient(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, id)
}

// broadcast sends a message to all registered SSE clients without blocking.
func (h *natsHub) broadcast(msg Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.clients {
		select {
		case ch <- msg:
		default:
			// drop message if client channel is full
		}
	}
}
