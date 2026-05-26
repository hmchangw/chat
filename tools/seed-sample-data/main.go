// Package main is the seed-sample-data CLI: populates MongoDB and Valkey
// with a small, well-formed, idempotent dataset for local development.
// Run via `make seed` after `make deps-up`.
package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	slog.Info("seed-sample-data placeholder")
}
