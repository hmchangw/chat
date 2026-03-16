package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the JetStream-to-NATS bridge.
type Config struct {
	// NATS connection
	NATSUrl string

	// JetStream source
	StreamName   string
	SubjectFilter string
	ConsumerName string

	// Core NATS publish target
	PublishSubject string

	// Dead-letter subject for messages that exceed max retries
	DeadLetterSubject string

	// Retry behavior
	MaxRetries    int
	RetryBaseDelay time.Duration

	// Pull consumer tuning
	FetchBatchSize int
	FetchTimeout   time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		NATSUrl:           envOrDefault("NATS_URL", "nats://localhost:4222"),
		StreamName:        envOrDefault("JS_STREAM_NAME", "CHAT"),
		SubjectFilter:     envOrDefault("JS_SUBJECT_FILTER", "chat.messages.>"),
		ConsumerName:      envOrDefault("JS_CONSUMER_NAME", "chat-bridge"),
		PublishSubject:    envOrDefault("PUBLISH_SUBJECT", "chat.delivered"),
		DeadLetterSubject: envOrDefault("DEAD_LETTER_SUBJECT", "chat.deadletter"),
		MaxRetries:        envOrDefaultInt("MAX_RETRIES", 3),
		RetryBaseDelay:    envOrDefaultDuration("RETRY_BASE_DELAY", 500*time.Millisecond),
		FetchBatchSize:    envOrDefaultInt("FETCH_BATCH_SIZE", 10),
		FetchTimeout:      envOrDefaultDuration("FETCH_TIMEOUT", 5*time.Second),
	}

	if cfg.StreamName == "" {
		return nil, fmt.Errorf("JS_STREAM_NAME is required")
	}
	if cfg.PublishSubject == "" {
		return nil, fmt.Errorf("PUBLISH_SUBJECT is required")
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envOrDefaultDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
