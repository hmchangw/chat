package main

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

// config holds every tunable for the oplog-connector, parsed from the
// environment via caarlos0/env. Required fields have no default and cause a
// fail-fast at startup when absent.
type config struct {
	SiteID string `env:"SITE_ID,required"`

	// Source legacy MongoDB (replica set) — change streams are read here AND
	// the checkpoint collection is written here.
	SourceMongoURI string `env:"SOURCE_MONGO_URI,required"`
	SourceUsername string `env:"SOURCE_MONGO_USERNAME" envDefault:""`
	SourcePassword string `env:"SOURCE_MONGO_PASSWORD" envDefault:""`
	SourceDB       string `env:"SOURCE_DB"            envDefault:"rocketchat"`
	CheckpointDB   string `env:"CHECKPOINT_DB"        envDefault:"migration"`

	NatsURL       string `env:"NATS_URL,required"`
	NatsCredsFile string `env:"NATS_CREDS_FILE" envDefault:""`

	WatchCollections    []string `env:"WATCH_COLLECTIONS,required"`
	PreimageCollections []string `env:"PREIMAGE_COLLECTIONS" envDefault:"rocketchat_message"`

	ReadPreference string `env:"READ_PREFERENCE" envDefault:"secondary"`

	// CheckpointEvery throttles checkpoint persistence: the resume token is
	// saved once every N acked events (and always on graceful shutdown).
	// Larger = fewer writes but more replay on crash (replays are deduped).
	CheckpointEvery int `env:"CHECKPOINT_EVERY" envDefault:"100"`

	// Start-point resolution (see resolveStartPoint / spec §4.2).
	StartMode        string `env:"START_MODE"         envDefault:"now"` // now | beginning | time
	StartAtTime      string `env:"START_AT_TIME"      envDefault:""`    // RFC3339 or unix-ms
	StartResumeToken string `env:"START_RESUME_TOKEN" envDefault:""`    // _data hex, one-off seed override

	Bootstrap bootstrapConfig `envPrefix:"BOOTSTRAP_"`

	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
}

// parseConfig parses and validates the environment configuration.
func parseConfig() (config, error) {
	cfg, err := env.ParseAs[config]()
	if err != nil {
		return config{}, fmt.Errorf("parse config: %w", err)
	}
	switch cfg.StartMode {
	case "now", "beginning", "time":
	default:
		return config{}, fmt.Errorf("invalid START_MODE %q (want now|beginning|time)", cfg.StartMode)
	}
	if cfg.StartMode == "time" && cfg.StartAtTime == "" {
		return config{}, fmt.Errorf("START_MODE=time requires START_AT_TIME")
	}
	if cfg.CheckpointEvery < 1 {
		return config{}, fmt.Errorf("CHECKPOINT_EVERY must be >= 1, got %d", cfg.CheckpointEvery)
	}
	return cfg, nil
}
