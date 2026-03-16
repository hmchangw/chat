package store

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gocql/gocql"
	"github.com/hmchangw/chat/internal/config"
	"github.com/hmchangw/chat/internal/model"
)

type CassandraStore struct {
	session *gocql.Session
}

func NewCassandraStore(cfg config.CassandraConfig) (*CassandraStore, error) {
	cluster := gocql.NewCluster(cfg.Hosts...)
	cluster.Keyspace = cfg.Keyspace
	cluster.Consistency = gocql.LocalQuorum
	if cfg.Username != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: cfg.Username,
			Password: cfg.Password,
		}
	}

	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("creating cassandra session: %w", err)
	}

	return &CassandraStore{session: session}, nil
}

const insertMessageQuery = `INSERT INTO messages (conversation_id, message_id, sender_id, body, created_at) VALUES (?, ?, ?, ?, ?)`

// WriteMessage inserts a single message into Cassandra.
func (s *CassandraStore) WriteMessage(ctx context.Context, msg model.Message) error {
	return s.session.Query(insertMessageQuery,
		msg.ConversationID,
		msg.MessageID,
		msg.SenderID,
		msg.Body,
		msg.CreatedAt,
	).WithContext(ctx).Exec()
}

// WriteBatch inserts multiple messages in an unlogged batch.
// Unlogged because messages typically span different partitions (conversation_id).
func (s *CassandraStore) WriteBatch(ctx context.Context, msgs []model.Message) error {
	if len(msgs) == 0 {
		return nil
	}

	batch := s.session.NewBatch(gocql.UnloggedBatch).WithContext(ctx)
	for _, msg := range msgs {
		batch.Query(insertMessageQuery,
			msg.ConversationID,
			msg.MessageID,
			msg.SenderID,
			msg.Body,
			msg.CreatedAt,
		)
	}

	if err := s.session.ExecuteBatch(batch); err != nil {
		return fmt.Errorf("executing batch insert (%d messages): %w", len(msgs), err)
	}
	slog.Debug("batch written", "count", len(msgs))
	return nil
}

func (s *CassandraStore) Close() {
	s.session.Close()
}
