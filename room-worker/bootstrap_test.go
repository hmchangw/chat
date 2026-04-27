package main

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"
)

type fakeStreamCreator struct {
	created []string
	failOn  string // stream name to fail on; empty = never fail
	failErr error  // error to return when failing
}

// Returns nil for the Stream value because bootstrapStreams discards it.
func (f *fakeStreamCreator) CreateOrUpdateStream(_ context.Context, cfg jetstream.StreamConfig) (oteljetstream.Stream, error) { //nolint:gocritic // hugeParam: cfg is passed by value to satisfy the streamCreator interface
	if f.failOn != "" && cfg.Name == f.failOn {
		return nil, f.failErr
	}
	f.created = append(f.created, cfg.Name)
	return nil, nil
}

func TestBootstrapStreams(t *testing.T) {
	tests := []struct {
		name        string
		enabled     bool
		failOn      string
		failErr     error
		wantCreated []string
		wantErrSub  string
	}{
		{
			name:        "disabled - skips creation",
			enabled:     false,
			wantCreated: nil,
		},
		{
			name:        "enabled - creates ROOMS",
			enabled:     true,
			wantCreated: []string{"ROOMS_test"},
		},
		{
			name:       "enabled - wraps ROOMS creator error",
			enabled:    true,
			failOn:     "ROOMS_test",
			failErr:    errors.New("nats down"),
			wantErrSub: "create ROOMS stream",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeStreamCreator{failOn: tc.failOn, failErr: tc.failErr}
			err := bootstrapStreams(context.Background(), fake, "test", tc.enabled)
			if tc.wantErrSub != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSub)
				assert.ErrorIs(t, err, tc.failErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantCreated, fake.created)
		})
	}
}
