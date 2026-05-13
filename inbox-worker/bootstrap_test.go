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

type fakeStreamManager struct {
	created     []jetstream.StreamConfig
	existing    map[string]bool                   // streams that "exist" for the disabled path
	existingCfg map[string]jetstream.StreamConfig // existing stream configs (for Sources preservation)
	failOn      string                            // stream name to fail on; empty = never fail
	failErr     error                             // error to return when failing
}

// Returns nil for the Stream value because bootstrapStreams discards it.
func (f *fakeStreamManager) CreateOrUpdateStream(_ context.Context, cfg jetstream.StreamConfig) (oteljetstream.Stream, error) { //nolint:gocritic // hugeParam: cfg is passed by value to satisfy the streamManager interface
	if f.failOn != "" && cfg.Name == f.failOn {
		return nil, f.failErr
	}
	f.created = append(f.created, cfg)
	return nil, nil
}

func (f *fakeStreamManager) Stream(_ context.Context, name string) (oteljetstream.Stream, error) {
	if f.existing[name] {
		if cfg, ok := f.existingCfg[name]; ok {
			return &fakeStream{cfg: cfg}, nil
		}
		return nil, nil
	}
	return nil, jetstream.ErrStreamNotFound
}

// fakeStream returns a canned StreamInfo so bootstrap can read existing
// federation Sources without needing a real oteljetstream.Stream impl.
// All other Stream methods are inherited from the embedded interface
// and will panic on nil if invoked -- bootstrap only calls CachedInfo.
type fakeStream struct {
	oteljetstream.Stream
	cfg jetstream.StreamConfig
}

func (s *fakeStream) CachedInfo() *jetstream.StreamInfo {
	return &jetstream.StreamInfo{Config: s.cfg}
}

func TestBootstrapStreams(t *testing.T) {
	preservedSources := []*jetstream.StreamSource{{
		Name: "OUTBOX_peer",
		SubjectTransforms: []jetstream.SubjectTransformConfig{{
			Source:      "outbox.peer.to.test.>",
			Destination: "chat.inbox.test.aggregate.>",
		}},
	}}

	tests := []struct {
		name         string
		enabled      bool
		existing     map[string]bool
		existingCfg  map[string]jetstream.StreamConfig
		failOn       string
		failErr      error
		wantCreated  []string
		wantSources  []*jetstream.StreamSource
		wantSubjects []string // when nil, defaults to stream.Inbox("test").Subjects
		wantErrSub   string
	}{
		{
			name:        "disabled - verifies existing stream",
			enabled:     false,
			existing:    map[string]bool{"INBOX_test": true},
			wantCreated: nil,
		},
		{
			name:       "disabled - fails when stream missing",
			enabled:    false,
			existing:   map[string]bool{},
			wantErrSub: "verify INBOX stream",
		},
		{
			name:        "enabled - creates INBOX with Name and Subjects (no existing stream)",
			enabled:     true,
			existing:    map[string]bool{},
			wantCreated: []string{"INBOX_test"},
		},
		{
			name:     "enabled - preserves existing federation Sources",
			enabled:  true,
			existing: map[string]bool{"INBOX_test": true},
			existingCfg: map[string]jetstream.StreamConfig{
				"INBOX_test": {Name: "INBOX_test", Sources: preservedSources},
			},
			wantCreated: []string{"INBOX_test"},
			wantSources: preservedSources,
		},
		{
			name:     "enabled - empty Sources on existing stream stays empty",
			enabled:  true,
			existing: map[string]bool{"INBOX_test": true},
			existingCfg: map[string]jetstream.StreamConfig{
				"INBOX_test": {Name: "INBOX_test"},
			},
			wantCreated: []string{"INBOX_test"},
		},
		{
			name:       "enabled - wraps INBOX creator error",
			enabled:    true,
			existing:   map[string]bool{},
			failOn:     "INBOX_test",
			failErr:    errors.New("nats down"),
			wantErrSub: "create INBOX stream",
		},
		{
			// Guards against narrowing the live stream's Subjects when ops/IaC
			// has layered on an extra subject for a future federation event
			// type. The schema-only update must preserve any superset of the
			// declared baseline.
			name:     "enabled - preserves existing extra subjects (union)",
			enabled:  true,
			existing: map[string]bool{"INBOX_test": true},
			existingCfg: map[string]jetstream.StreamConfig{
				"INBOX_test": {
					Name: "INBOX_test",
					Subjects: []string{
						"chat.inbox.test.aggregate.>",
						"chat.inbox.test.extra.>",
					},
				},
			},
			wantCreated: []string{"INBOX_test"},
			wantSubjects: append(
				append([]string(nil), stream.Inbox("test").Subjects...),
				"chat.inbox.test.extra.>",
			),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeStreamManager{
				failOn:      tc.failOn,
				failErr:     tc.failErr,
				existing:    tc.existing,
				existingCfg: tc.existingCfg,
			}
			err := bootstrapStreams(context.Background(), fake, "test", tc.enabled)
			if tc.wantErrSub != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSub)
				if tc.enabled {
					assert.ErrorIs(t, err, tc.failErr)
				} else {
					assert.ErrorIs(t, err, jetstream.ErrStreamNotFound)
				}
				return
			}
			require.NoError(t, err)
			require.Len(t, fake.created, len(tc.wantCreated))
			wantSubjects := tc.wantSubjects
			if wantSubjects == nil {
				wantSubjects = stream.Inbox("test").Subjects
			}
			for i, wantName := range tc.wantCreated {
				assert.Equal(t, wantName, fake.created[i].Name)
				// Schema is authored here; Sources and any extra live Subjects
				// must be preserved verbatim (union for Subjects).
				assert.Equal(t, wantSubjects, fake.created[i].Subjects,
					"INBOX bootstrap must set Subjects to the union of "+
						"pkg/stream.Inbox + any existing extras")
				assert.Equal(t, tc.wantSources, fake.created[i].Sources,
					"INBOX bootstrap must preserve existing Sources verbatim "+
						"and never invent Sources of its own")
			}
		})
	}
}
