package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hmchangw/chat/internal/config"
	"github.com/hmchangw/chat/internal/model"
	"github.com/hmchangw/chat/internal/store"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type Consumer struct {
	js    jetstream.JetStream
	store *store.CassandraStore
	cfg   config.Config
	nc    *nats.Conn
}

func New(cfg config.Config, store *store.CassandraStore) (*Consumer, error) {
	nc, err := nats.Connect(cfg.NATS.URL)
	if err != nil {
		return nil, fmt.Errorf("connecting to nats: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("creating jetstream context: %w", err)
	}

	return &Consumer{
		js:    js,
		store: store,
		cfg:   cfg,
		nc:    nc,
	}, nil
}

// Run starts the pull consumer loop. It blocks until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	cons, err := c.js.CreateOrUpdateConsumer(ctx, c.cfg.NATS.Stream, jetstream.ConsumerConfig{
		Durable:       c.cfg.NATS.Durable,
		FilterSubject: c.cfg.NATS.Subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    5,
		AckWait:       30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("creating consumer: %w", err)
	}

	slog.Info("consumer started",
		"stream", c.cfg.NATS.Stream,
		"durable", c.cfg.NATS.Durable,
		"batch_size", c.cfg.Consumer.BatchSize,
	)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := c.fetchAndWrite(ctx, cons); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Error("fetch cycle failed", "error", err)
			time.Sleep(time.Second)
		}
	}
}

func (c *Consumer) fetchAndWrite(ctx context.Context, cons jetstream.Consumer) error {
	batch, err := cons.Fetch(c.cfg.Consumer.BatchSize,
		jetstream.FetchMaxWait(c.cfg.Consumer.FlushTimeout),
	)
	if err != nil {
		return fmt.Errorf("fetching messages: %w", err)
	}

	var msgs []model.Message
	var jsMsgs []jetstream.Msg

	for msg := range batch.Messages() {
		var m model.Message
		if err := json.Unmarshal(msg.Data(), &m); err != nil {
			slog.Warn("skipping malformed message", "error", err, "subject", msg.Subject())
			// Ack to avoid infinite redelivery of unparseable messages.
			_ = msg.Ack()
			continue
		}
		msgs = append(msgs, m)
		jsMsgs = append(jsMsgs, msg)
	}

	if batch.Error() != nil {
		return fmt.Errorf("batch error: %w", batch.Error())
	}

	if len(msgs) == 0 {
		return nil
	}

	if err := c.store.WriteBatch(ctx, msgs); err != nil {
		// Nak all so JetStream redelivers them.
		for _, msg := range jsMsgs {
			_ = msg.Nak()
		}
		return fmt.Errorf("writing batch to cassandra: %w", err)
	}

	for _, msg := range jsMsgs {
		_ = msg.Ack()
	}
	slog.Info("batch processed", "count", len(msgs))
	return nil
}

func (c *Consumer) Close() {
	c.nc.Close()
}
