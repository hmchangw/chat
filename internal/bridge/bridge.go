package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hmchangw/chat/internal/config"
	"github.com/hmchangw/chat/internal/message"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Bridge pulls messages from a JetStream consumer, enriches them,
// and publishes to a Core NATS subject. Failed messages are retried
// with exponential backoff, then routed to a dead-letter subject.
type Bridge struct {
	nc       *nats.Conn
	js       jetstream.JetStream
	consumer jetstream.Consumer
	cfg      *config.Config
	logger   *slog.Logger
}

// New creates a Bridge and ensures the durable pull consumer exists.
func New(ctx context.Context, nc *nats.Conn, cfg *config.Config, logger *slog.Logger) (*Bridge, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}

	consumer, err := js.CreateOrUpdateConsumer(ctx, cfg.StreamName, jetstream.ConsumerConfig{
		Durable:       cfg.ConsumerName,
		FilterSubject: cfg.SubjectFilter,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    cfg.MaxRetries + 1,
		AckWait:       30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create consumer %q on stream %q: %w", cfg.ConsumerName, cfg.StreamName, err)
	}

	return &Bridge{
		nc:       nc,
		js:       js,
		consumer: consumer,
		cfg:      cfg,
		logger:   logger,
	}, nil
}

// Run pulls messages in batches until the context is cancelled.
func (b *Bridge) Run(ctx context.Context) error {
	b.logger.Info("bridge started",
		"stream", b.cfg.StreamName,
		"consumer", b.cfg.ConsumerName,
		"publish_subject", b.cfg.PublishSubject,
	)

	for {
		select {
		case <-ctx.Done():
			b.logger.Info("bridge stopping")
			return ctx.Err()
		default:
		}

		msgs, err := b.consumer.Fetch(b.cfg.FetchBatchSize,
			jetstream.FetchMaxWait(b.cfg.FetchTimeout),
		)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b.logger.Warn("fetch error, retrying", "error", err)
			time.Sleep(time.Second)
			continue
		}

		for msg := range msgs.Messages() {
			b.processMessage(msg)
		}

		if msgs.Error() != nil {
			b.logger.Warn("batch error", "error", msgs.Error())
		}
	}
}

func (b *Bridge) processMessage(msg jetstream.Msg) {
	meta, _ := msg.Metadata()

	enriched, err := message.Enrich(msg)
	if err != nil {
		b.logger.Error("enrich failed", "subject", msg.Subject(), "error", err)
		b.deadLetter(msg, fmt.Sprintf("enrich error: %v", err))
		return
	}

	err = b.nc.Publish(b.cfg.PublishSubject, enriched)
	if err != nil {
		deliveryAttempt := uint64(1)
		if meta != nil {
			deliveryAttempt = meta.NumDelivered
		}

		if deliveryAttempt >= uint64(b.cfg.MaxRetries) {
			b.logger.Error("publish failed, sending to dead letter",
				"subject", msg.Subject(),
				"attempts", deliveryAttempt,
				"error", err,
			)
			b.deadLetter(msg, fmt.Sprintf("publish error after %d attempts: %v", deliveryAttempt, err))
			return
		}

		delay := b.cfg.RetryBaseDelay * time.Duration(1<<(deliveryAttempt-1))
		b.logger.Warn("publish failed, scheduling retry",
			"subject", msg.Subject(),
			"attempt", deliveryAttempt,
			"retry_delay", delay,
			"error", err,
		)
		_ = msg.NakWithDelay(delay)
		return
	}

	if err := msg.Ack(); err != nil {
		b.logger.Error("ack failed", "subject", msg.Subject(), "error", err)
	} else {
		seq := uint64(0)
		if meta != nil {
			seq = meta.Sequence.Stream
		}
		b.logger.Debug("bridged message",
			"source", msg.Subject(),
			"target", b.cfg.PublishSubject,
			"stream_seq", seq,
		)
	}
}

func (b *Bridge) deadLetter(msg jetstream.Msg, reason string) {
	hdrs := nats.Header{}
	hdrs.Set("X-Dead-Letter-Reason", reason)
	hdrs.Set("X-Original-Subject", msg.Subject())

	dlMsg := &nats.Msg{
		Subject: b.cfg.DeadLetterSubject,
		Header:  hdrs,
		Data:    msg.Data(),
	}

	if err := b.nc.PublishMsg(dlMsg); err != nil {
		b.logger.Error("dead letter publish failed",
			"subject", msg.Subject(),
			"error", err,
		)
	}

	// Terminate redelivery — this message is done.
	_ = msg.Term()
}
