package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	// TODO: wire config, MongoDB, MinIO, NATS, and HTTP server (avatar-service Task 4+).
}
