package config

import "github.com/caarlos0/env/v11"

// CassandraConfig holds Cassandra connection settings.
// Env vars: CASSANDRA_HOSTS, CASSANDRA_KEYSPACE
type CassandraConfig struct {
	Hosts    string `env:"HOSTS"    required:"true"`
	Keyspace string `env:"KEYSPACE" envDefault:"chat"`
}

// MongoConfig holds MongoDB connection settings.
// Env vars: MONGO_URI, MONGO_DB
type MongoConfig struct {
	URI string `env:"URI" required:"true"`
	DB  string `env:"DB"  envDefault:"chat"`
}

// NATSConfig holds NATS connection settings.
// Env vars: NATS_URL
type NATSConfig struct {
	URL string `env:"URL" required:"true"`
}

// Config is the top-level configuration for history-service.
type Config struct {
	SiteID    string          `env:"SITE_ID" envDefault:"site-local"`
	Cassandra CassandraConfig `envPrefix:"CASSANDRA_"`
	Mongo     MongoConfig     `envPrefix:"MONGO_"`
	NATS      NATSConfig      `envPrefix:"NATS_"`
}

// Load parses environment variables into Config. Returns error if required vars are missing.
func Load() (Config, error) {
	return env.ParseAs[Config]()
}
