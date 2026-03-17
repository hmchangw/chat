package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Message represents a chat message consumed from JetStream.
type Message struct {
	RoomID    string    `json:"room_id"`
	UserID    string    `json:"user_id"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// StartRouter sets up the JetStream stream, consumer, and starts routing
// messages from chat.messages to per-room core NATS subjects. It returns
// a stop function and any error encountered during setup.
func StartRouter(ctx context.Context, nc *nats.Conn) (stop func(), err error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}

	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "CHAT",
		Subjects: []string{"chat.messages"},
	})
	if err != nil {
		return nil, fmt.Errorf("create/update stream: %w", err)
	}

	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:   "room-router",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create consumer: %w", err)
	}

	cctx, err := cons.Consume(func(msg jetstream.Msg) {
		var chatMsg Message
		if err := json.Unmarshal(msg.Data(), &chatMsg); err != nil {
			log.Printf("Failed to unmarshal message: %v", err)
			msg.Term()
			return
		}

		subject := fmt.Sprintf("chat.room.%s", chatMsg.RoomID)
		if err := nc.Publish(subject, msg.Data()); err != nil {
			log.Printf("Failed to publish to %s: %v", subject, err)
			msg.Nak()
			return
		}

		msg.Ack()
		log.Printf("Routed message from user %s to %s", chatMsg.UserID, subject)
	})
	if err != nil {
		return nil, fmt.Errorf("start consumer: %w", err)
	}

	return cctx.Stop, nil
}

func main() {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("Failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Println("Starting JetStream room router...")

	stop, err := StartRouter(ctx, nc)
	if err != nil {
		log.Fatalf("Failed to start router: %v", err)
	}
	defer stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down...")
}
