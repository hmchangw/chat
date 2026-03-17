package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hmchangw/chat/internal/handler"
	"github.com/hmchangw/chat/internal/repository"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	hosts := strings.Split(getEnv("CASSANDRA_HOSTS", "127.0.0.1"), ",")
	port, _ := strconv.Atoi(getEnv("CASSANDRA_PORT", "9042"))

	cfg := repository.CassandraConfig{
		Hosts:    hosts,
		Port:     port,
		Username: os.Getenv("CASSANDRA_USERNAME"),
		Password: os.Getenv("CASSANDRA_PASSWORD"),
		Timeout:  10 * time.Second,
	}

	session, err := repository.NewSession(cfg)
	if err != nil {
		log.Fatalf("failed to connect to cassandra: %v", err)
	}
	defer session.Close()

	log.Println("Connected to Cassandra")

	msgRepo := repository.NewMessageRepository(session)
	msgHandler := handler.NewMessageHandler(msgRepo)

	mux := http.NewServeMux()
	mux.Handle("/messages", msgHandler)
	mux.Handle("/messages/", msgHandler)

	addr := getEnv("SERVER_ADDR", ":8080")
	log.Printf("Server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
