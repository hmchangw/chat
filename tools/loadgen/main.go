package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/caarlos0/env/v11"
)

type config struct {
	NatsURL       string `env:"NATS_URL,required"`
	NatsCredsFile string `env:"NATS_CREDS_FILE" envDefault:""`
	SiteID        string `env:"SITE_ID"         envDefault:"site-local"`
	MongoURI      string `env:"MONGO_URI,required"`
	MongoDB       string `env:"MONGO_DB"        envDefault:"chat"`
	MetricsAddr   string `env:"METRICS_ADDR"    envDefault:":9099"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: loadgen <seed|run|teardown> [flags]")
		os.Exit(2)
	}
	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}
	_ = cfg
	switch os.Args[1] {
	case "seed", "run", "teardown":
		slog.Info("subcommand not yet implemented", "subcommand", os.Args[1])
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		os.Exit(2)
	}
}
