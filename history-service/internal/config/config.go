package config

import "github.com/caarlos0/env/v11"

// CassandraConfig holds Cassandra connection settings.
type CassandraConfig struct {
	Hosts    string `env:"CASSANDRA_HOSTS"    required:"true"`
	Keyspace string `env:"CASSANDRA_KEYSPACE" envDefault:"chat"`
}

// MongoConfig holds MongoDB connection settings.
type MongoConfig struct {
	URI string `env:"MONGO_URI" required:"true"`
	DB  string `env:"MONGO_DB"  envDefault:"chat"`
}

// NATSConfig holds NATS connection settings.
type NATSConfig struct {
	URL string `env:"NATS_URL" required:"true"`
}

// Config is the top-level configuration for history-service.
type Config struct {
	SiteID    string          `env:"SITE_ID" envDefault:"site-local"`
	Cassandra CassandraConfig `envPrefix:""`
	Mongo     MongoConfig     `envPrefix:""`
	NATS      NATSConfig      `envPrefix:""`
}

// Load parses environment variables into Config. Returns error if required vars are missing.
func Load() (Config, error) {
	return env.ParseAs[Config]()
}
