package config

import (
	"github.com/caarlos0/env/v11"

	"github.com/hmchangw/chat/pkg/atrest"
)

// CassandraConfig holds Cassandra connection settings (env prefix: CASSANDRA_).
type CassandraConfig struct {
	Hosts    string `env:"HOSTS"    required:"true"`
	Keyspace string `env:"KEYSPACE" envDefault:"chat"`
	Username string `env:"USERNAME" envDefault:""`
	Password string `env:"PASSWORD" envDefault:""`
	// NumConns sets gocql's per-host connection count; zero lets cassutil apply its own default.
	NumConns int `env:"NUM_CONNS" envDefault:"8"`
}

// MongoConfig holds MongoDB connection settings (env prefix: MONGO_).
type MongoConfig struct {
	URI      string `env:"URI"      required:"true"`
	DB       string `env:"DB"       envDefault:"chat"`
	Username string `env:"USERNAME" envDefault:""`
	Password string `env:"PASSWORD" envDefault:""`
}

// NATSConfig holds NATS connection settings (env prefix: NATS_).
type NATSConfig struct {
	URL       string `env:"URL" required:"true"`
	CredsFile string `env:"CREDS_FILE" envDefault:""`
}

// Config is the top-level configuration for the history-service.
type Config struct {
	SiteID                  string             `env:"SITE_ID"                    envDefault:"site-local"`
	Cassandra               CassandraConfig    `envPrefix:"CASSANDRA_"`
	Mongo                   MongoConfig        `envPrefix:"MONGO_"`
	NATS                    NATSConfig         `envPrefix:"NATS_"`
	MessageBucketHours      int                `env:"MESSAGE_BUCKET_HOURS"       envDefault:"72"`
	MessageReadMaxBuckets   int                `env:"MESSAGE_READ_MAX_BUCKETS"   envDefault:"122"`
	MessageHistoryFloorDays int                `env:"MESSAGE_HISTORY_FLOOR_DAYS" envDefault:"365"`
	Atrest                  atrest.Config      // env vars are already prefixed ATREST_*
	Vault                   atrest.VaultConfig // env vars are already prefixed (VAULT_*, ATREST_VAULT_*)
}

// Load parses environment variables into Config; returns an error when required vars are absent.
func Load() (Config, error) {
	return env.ParseAs[Config]()
}
