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

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("Failed to create JetStream context: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Ensure the stream exists. Create it if it doesn't.
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "CHAT",
		Subjects: []string{"chat.messages"},
	})
	if err != nil {
		log.Fatalf("Failed to create/update stream: %v", err)
	}

	// Create a durable consumer so we resume from where we left off across restarts.
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:   "room-router",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("Failed to create consumer: %v", err)
	}

	log.Println("Starting JetStream room router...")

	// Consume messages and route them to per-room core NATS subjects.
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
		log.Fatalf("Failed to start consumer: %v", err)
	}
	defer cctx.Stop()

	// Wait for shutdown signal.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down...")
}
