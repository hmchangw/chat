package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// MongoConfig holds MongoDB connection settings (env prefix: MONGO_).
type MongoConfig struct {
	URI      string `env:"URI,notEmpty"`
	DB       string `env:"DB"       envDefault:"chat"`
	Username string `env:"USERNAME" envDefault:""`
	Password string `env:"PASSWORD" envDefault:""`
}

// NATSConfig holds NATS connection settings (env prefix: NATS_).
type NATSConfig struct {
	URL       string `env:"URL,notEmpty"`
	CredsFile string `env:"CREDS_FILE" envDefault:""`
}

// Config is the top-level configuration for user-service.
type Config struct {
	// SiteID is load-bearing for federation — it is baked into every
	// subscription subject pattern and into the outbox source/dest on
	// publishStatus. A missing SITE_ID silently subscribing under a default
	// would be far harder to diagnose than a fast-fail, so it is required.
	SiteID               string        `env:"SITE_ID,notEmpty"`
	AllSiteIDs           []string      `env:"ALL_SITE_IDS"           envDefault:"" envSeparator:","`
	MaxSubscriptionLimit int           `env:"MAX_SUBSCRIPTION_LIMIT" envDefault:"1000"`
	MetricsAddr          string        `env:"METRICS_ADDR"           envDefault:":9090"`
	HandlerTimeout       time.Duration `env:"HANDLER_TIMEOUT"        envDefault:"15s"`
	Mongo                MongoConfig   `envPrefix:"MONGO_"`
	NATS                 NATSConfig    `envPrefix:"NATS_"`
}

// Load parses environment variables into Config; returns an error when required
// vars are absent or MAX_SUBSCRIPTION_LIMIT is non-positive (a 0/negative limit
// makes the aggregation $limit stage error at query time — fail fast instead).
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("parse user-service config: %w", err)
	}
	if cfg.MaxSubscriptionLimit < 1 {
		return Config{}, fmt.Errorf("MAX_SUBSCRIPTION_LIMIT must be >= 1, got %d", cfg.MaxSubscriptionLimit)
	}
	return cfg, nil
}
