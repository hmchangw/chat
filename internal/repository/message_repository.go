package repository

import (
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/hmchangw/chat/internal/model"
)

type MessageRepository struct {
	session *gocql.Session
}

func NewMessageRepository(session *gocql.Session) *MessageRepository {
	return &MessageRepository{session: session}
}

func (r *MessageRepository) Create(req model.CreateMessageRequest) (*model.Message, error) {
	msg := &model.Message{
		ID:        uuid.New(),
		Sender:    req.Sender,
		Content:   req.Content,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	query := `INSERT INTO messages (id, sender, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`
	if err := r.session.Query(query,
		msg.ID, msg.Sender, msg.Content, msg.CreatedAt, msg.UpdatedAt,
	).Exec(); err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}

	return msg, nil
}

func (r *MessageRepository) GetByID(id uuid.UUID) (*model.Message, error) {
	msg := &model.Message{}
	query := `SELECT id, sender, content, created_at, updated_at FROM messages WHERE id = ?`
	if err := r.session.Query(query, id).Scan(
		&msg.ID, &msg.Sender, &msg.Content, &msg.CreatedAt, &msg.UpdatedAt,
	); err != nil {
		if err == gocql.ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get message: %w", err)
	}

	return msg, nil
}

func (r *MessageRepository) List() ([]*model.Message, error) {
	query := `SELECT id, sender, content, created_at, updated_at FROM messages`
	iter := r.session.Query(query).Iter()

	var messages []*model.Message
	msg := &model.Message{}
	for iter.Scan(&msg.ID, &msg.Sender, &msg.Content, &msg.CreatedAt, &msg.UpdatedAt) {
		messages = append(messages, msg)
		msg = &model.Message{}
	}

	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}

	return messages, nil
}

func (r *MessageRepository) Update(id uuid.UUID, req model.UpdateMessageRequest) (*model.Message, error) {
	now := time.Now().UTC()
	query := `UPDATE messages SET content = ?, updated_at = ? WHERE id = ?`
	if err := r.session.Query(query, req.Content, now, id).Exec(); err != nil {
		return nil, fmt.Errorf("failed to update message: %w", err)
	}

	return r.GetByID(id)
}

func (r *MessageRepository) Delete(id uuid.UUID) error {
	query := `DELETE FROM messages WHERE id = ?`
	if err := r.session.Query(query, id).Exec(); err != nil {
		return fmt.Errorf("failed to delete message: %w", err)
	}

	return nil
}
