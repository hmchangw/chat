package main

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"

	"github.com/hmchangw/chat/pkg/stream"
)

type fakeStreamCreator struct {
	created []jetstream.StreamConfig
	failOn  string // stream name to fail on; empty = never fail
	failErr error  // error to return when failing
}

// Returns nil for the Stream value because bootstrapStreams discards it.
func (f *fakeStreamCreator) CreateOrUpdateStream(_ context.Context, cfg jetstream.StreamConfig) (oteljetstream.Stream, error) { //nolint:gocritic // hugeParam: cfg is passed by value to satisfy the streamCreator interface
	if f.failOn != "" && cfg.Name == f.failOn {
		return nil, f.failErr
	}
	f.created = append(f.created, cfg)
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
			name:        "enabled - creates INBOX with Name and Subjects",
			enabled:     true,
			wantCreated: []string{"INBOX_test"},
		},
		{
			name:       "enabled - wraps INBOX creator error",
			enabled:    true,
			failOn:     "INBOX_test",
			failErr:    errors.New("nats down"),
			wantErrSub: "create INBOX stream",
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
			require.Len(t, fake.created, len(tc.wantCreated))
			wantSubjects := stream.Inbox("test").Subjects
			for i, wantName := range tc.wantCreated {
				assert.Equal(t, wantName, fake.created[i].Name)
				// App owns the schema (Name + Subjects). Federation
				// (Sources + SubjectTransforms) belongs to ops/IaC and
				// must not appear here.
				assert.Equal(t, wantSubjects, fake.created[i].Subjects,
					"INBOX bootstrap must set Subjects from pkg/stream.Inbox")
				assert.Empty(t, fake.created[i].Sources,
					"federation Sources are owned by ops/IaC and must not be set in app code")
			}
		})
	}
}
