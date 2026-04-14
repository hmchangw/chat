package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInboxBootstrapStreamConfig(t *testing.T) {
	t.Run("baseline name and subjects come from pkg/stream.Inbox", func(t *testing.T) {
		cfg := inboxBootstrapStreamConfig("site-a", nil)
		assert.Equal(t, "INBOX_site-a", cfg.Name)
		assert.Equal(t, []string{
			"chat.inbox.site-a.*",
			"chat.inbox.site-a.aggregate.>",
		}, cfg.Subjects)
		assert.Empty(t, cfg.Sources, "no remote sites → no sources")
	})

	t.Run("one source per remote site with subject transform", func(t *testing.T) {
		cfg := inboxBootstrapStreamConfig("site-a", []string{"site-b", "site-c"})

		assert.Equal(t, "INBOX_site-a", cfg.Name)
		assert.Equal(t, []string{
			"chat.inbox.site-a.*",
			"chat.inbox.site-a.aggregate.>",
		}, cfg.Subjects)
		require.Len(t, cfg.Sources, 2)

		// site-b source — FilterSubject is intentionally empty because it's
		// mutually exclusive with SubjectTransforms; the transform's Source
		// field acts as the filter.
		assert.Equal(t, "OUTBOX_site-b", cfg.Sources[0].Name)
		assert.Empty(t, cfg.Sources[0].FilterSubject)
		require.Len(t, cfg.Sources[0].SubjectTransforms, 1)
		assert.Equal(t, "outbox.site-b.to.site-a.>", cfg.Sources[0].SubjectTransforms[0].Source)
		assert.Equal(t, "chat.inbox.site-a.aggregate.>", cfg.Sources[0].SubjectTransforms[0].Destination)

		// site-c source
		assert.Equal(t, "OUTBOX_site-c", cfg.Sources[1].Name)
		assert.Empty(t, cfg.Sources[1].FilterSubject)
		require.Len(t, cfg.Sources[1].SubjectTransforms, 1)
		assert.Equal(t, "outbox.site-c.to.site-a.>", cfg.Sources[1].SubjectTransforms[0].Source)
		assert.Equal(t, "chat.inbox.site-a.aggregate.>", cfg.Sources[1].SubjectTransforms[0].Destination)
	})
}
