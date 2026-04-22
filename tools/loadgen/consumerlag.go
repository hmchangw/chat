package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// ConsumerSampler polls a single durable consumer's info every interval and
// records min/peak/final samples. Start with Run(ctx); stop by cancelling ctx.
type ConsumerSampler struct {
	js       jetstream.JetStream
	stream   string
	durable  string
	metrics  *Metrics
	interval time.Duration

	hasSample        bool
	minPending       uint64
	peakPending      uint64
	finalPending     uint64
	peakAckPending   uint64
	finalRedelivered uint64
}

// NewConsumerSampler constructs a sampler.
func NewConsumerSampler(js jetstream.JetStream, stream, durable string, m *Metrics, interval time.Duration) *ConsumerSampler {
	return &ConsumerSampler{js: js, stream: stream, durable: durable, metrics: m, interval: interval}
}

// Run polls ConsumerInfo until ctx is cancelled.
func (s *ConsumerSampler) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sampleOnce(ctx)
		}
	}
}

func (s *ConsumerSampler) sampleOnce(ctx context.Context) {
	cons, err := s.js.Consumer(ctx, s.stream, s.durable)
	if err != nil {
		slog.Warn("consumer lookup failed", "stream", s.stream, "durable", s.durable, "error", err)
		return
	}
	info, err := cons.Info(ctx)
	if err != nil {
		slog.Warn("consumer info failed", "stream", s.stream, "durable", s.durable, "error", err)
		return
	}
	pending := info.NumPending
	ack := uint64(info.NumAckPending)
	redel := uint64(info.NumRedelivered)

	s.metrics.ConsumerPending.WithLabelValues(s.stream, s.durable).Set(float64(pending))
	s.metrics.ConsumerAckPending.WithLabelValues(s.stream, s.durable).Set(float64(ack))
	s.metrics.ConsumerRedelivered.WithLabelValues(s.stream, s.durable).Set(float64(redel))

	if !s.hasSample {
		s.hasSample = true
		s.minPending = pending
		s.peakPending = pending
		s.peakAckPending = ack
	} else {
		if pending < s.minPending {
			s.minPending = pending
		}
		if pending > s.peakPending {
			s.peakPending = pending
		}
		if ack > s.peakAckPending {
			s.peakAckPending = ack
		}
	}
	s.finalPending = pending
	s.finalRedelivered = redel
}

// Snapshot returns a ConsumerStat from what has been observed so far.
// Must only be called after Run has returned (i.e., after the context
// passed to Run has been cancelled and its goroutine has exited);
// concurrent calls to Snapshot while Run is still ticking are unsafe.
func (s *ConsumerSampler) Snapshot() ConsumerStat {
	return ConsumerStat{
		Stream:         s.stream,
		Durable:        s.durable,
		MinPending:     s.minPending,
		PeakPending:    s.peakPending,
		FinalPending:   s.finalPending,
		PeakAckPending: s.peakAckPending,
		Redelivered:    s.finalRedelivered,
	}
}
