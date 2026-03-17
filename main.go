package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hmchangw/chat/server"
	"github.com/nats-io/nats.go"
)

func main() {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL // nats://127.0.0.1:4222
	}

	srv, err := server.New(server.Config{NATSUrl: natsURL})
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	if err := srv.Start(); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}

	log.Println("chat server running, press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	srv.Shutdown()
}
