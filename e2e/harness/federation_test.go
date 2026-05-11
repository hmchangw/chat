//go:build e2e

package harness

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/stream"
)

// TestFederationConfigShape pins the federation contract documented in
// docs/superpowers/specs/2026-04-27-inbox-stream-ownership-design.md:
//
//	Source      = outbox.{peer}.to.{local}.>
//	Destination = chat.inbox.{local}.aggregate.>
//	Stream      = INBOX_{local} with subjects from pkg/stream.Inbox({local})
//
// A future refactor that changes any of these silently breaks federation;
// this test fails loudly in that case.
func TestFederationConfigShape(t *testing.T) {
	local := "siteA"
	peer := "siteB"

	wantSrc := fmt.Sprintf("outbox.%s.to.%s.>", peer, local)
	wantDst := fmt.Sprintf("chat.inbox.%s.aggregate.>", local)

	assert.Equal(t, "outbox.siteB.to.siteA.>", wantSrc)
	assert.Equal(t, "chat.inbox.siteA.aggregate.>", wantDst)
	assert.Equal(t, []string{
		"chat.inbox.siteA.*",
		"chat.inbox.siteA.aggregate.>",
	}, stream.Inbox(local).Subjects)
	assert.Equal(t, "INBOX_siteA", stream.Inbox(local).Name)
	assert.Equal(t, "OUTBOX_siteA", stream.Outbox(local).Name)
}
