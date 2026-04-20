package natsutil

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
	"github.com/nats-io/nats.go"
)

const defaultReconnectWait = 2 * time.Second

// Connect opens a traced NATS connection with sensible reconnect defaults.
// The NATS client name is taken from the HOSTNAME env var (pod name in
// Kubernetes, container ID in Docker). When credsFile is non-empty it is
// mounted as the user credentials (JWT + NKey); when empty the connection
// authenticates without credentials. Extra opts are appended and override any
// same-kind default.
func Connect(url, credsFile string, opts ...nats.Option) (*otelnats.Conn, error) {
	if credsFile != "" {
		if _, err := os.Stat(credsFile); err != nil {
			return nil, fmt.Errorf("nats creds file %q: %w", credsFile, err)
		}
	}

	name := os.Getenv("HOSTNAME")
	log := slog.With("component", "nats", "name", name)
	baseOpts := []nats.Option{
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(defaultReconnectWait),
		nats.RetryOnFailedConnect(true),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn("nats disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			log.Info("nats reconnected", "url", c.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			log.Warn("nats connection closed")
		}),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			log.Error("nats async error", "error", err)
		}),
	}
	baseOpts = append(baseOpts, opts...)

	if credsFile == "" {
		return otelnats.ConnectWithOptions(url, baseOpts)
	}
	return otelnats.ConnectWithCredentialsWithOptions(url, credsFile, baseOpts)
}
