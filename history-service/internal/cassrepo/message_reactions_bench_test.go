//go:build integration

package cassrepo

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/msgbucket"
)

func BenchmarkGetReactionsByMessageIDs(b *testing.B) {
	session := setupCassandra(b)
	repo := NewRepository(session, msgbucket.New(24*time.Hour), 365, 50)
	ctx := context.Background()

	alice := cassandra.Participant{ID: "u1", Account: "alice"}
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = fmt.Sprintf("bench-%03d", i)
		for j := 0; j < 5; j++ {
			emoji := fmt.Sprintf("e%d", j)
			require.NoError(b, session.Query(
				`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
				ids[i], emoji, []cassandra.Participant{alice},
			).Exec())
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.GetReactionsByMessageIDs(ctx, ids); err != nil {
			b.Fatal(err)
		}
	}
}
