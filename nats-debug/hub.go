package main

import "time"

// Message represents a NATS message received on a subscribed subject.
type Message struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`
	Payload   string    `json:"payload"`
	Timestamp time.Time `json:"timestamp"`
}

// Subscription represents an active NATS subscription on the dest server.
type Subscription struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
}

// ConnectionStatus holds the current connection state for both NATS servers.
type ConnectionStatus struct {
	SourceConnected bool   `json:"sourceConnected"`
	DestConnected   bool   `json:"destConnected"`
	SourceURL       string `json:"sourceURL,omitempty"`
	DestURL         string `json:"destURL,omitempty"`
}

// Hub manages connections to two NATS servers, subscriptions on the dest server,
// publishing to the source server, and fan-out of received messages to SSE clients.
//
//go:generate mockgen -destination=mock_hub_test.go -package=main . Hub
type Hub interface {
	// Connect establishes connections to both NATS servers. It disconnects any
	// existing connections first.
	Connect(sourceURL, destURL string) error

	// Disconnect closes both NATS connections and removes all subscriptions.
	Disconnect()

	// Subscribe adds a NATS subscription on the dest server. Returns the new
	// Subscription with its assigned ID.
	Subscribe(subject string) (Subscription, error)

	// Unsubscribe removes a subscription by ID. Returns an error if not found.
	Unsubscribe(id string) error

	// Publish sends a message payload to the given subject on the source server.
	Publish(subject, payload string) error

	// Status returns the current connection status for both servers.
	Status() ConnectionStatus

	// Subscriptions returns a snapshot of all active subscriptions.
	Subscriptions() []Subscription

	// RegisterSSEClient adds a channel that will receive all incoming messages.
	// Returns a unique client ID used to unregister later.
	RegisterSSEClient(ch chan<- Message) string

	// UnregisterSSEClient removes the SSE client with the given ID.
	UnregisterSSEClient(id string)
}
