package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	NATS      NATSConfig
	Cassandra CassandraConfig
	Consumer  ConsumerConfig
}

type NATSConfig struct {
	URL        string
	Stream     string
	Subject    string
	Durable    string
	MaxPending int
}

type CassandraConfig struct {
	Hosts    []string
	Keyspace string
	Username string
	Password string
}

type ConsumerConfig struct {
	BatchSize    int
	FlushTimeout time.Duration
}

func Load() Config {
	return Config{
		NATS: NATSConfig{
			URL:        envOrDefault("NATS_URL", "nats://localhost:4222"),
			Stream:     envOrDefault("NATS_STREAM", "MESSAGES"),
			Subject:    envOrDefault("NATS_SUBJECT", "chat.messages.>"),
			Durable:    envOrDefault("NATS_DURABLE", "cassandra-writer"),
			MaxPending: envIntOrDefault("NATS_MAX_PENDING", 256),
		},
		Cassandra: CassandraConfig{
			Hosts:    strings.Split(envOrDefault("CASSANDRA_HOSTS", "localhost"), ","),
			Keyspace: envOrDefault("CASSANDRA_KEYSPACE", "chat"),
			Username: os.Getenv("CASSANDRA_USERNAME"),
			Password: os.Getenv("CASSANDRA_PASSWORD"),
		},
		Consumer: ConsumerConfig{
			BatchSize:    envIntOrDefault("CONSUMER_BATCH_SIZE", 100),
			FlushTimeout: envDurationOrDefault("CONSUMER_FLUSH_TIMEOUT", 500*time.Millisecond),
		},
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOrDefault(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}

func envDurationOrDefault(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return fallback
}
