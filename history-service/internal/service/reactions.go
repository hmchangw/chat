package service

import (
	"context"
	"fmt"

	"github.com/hmchangw/chat/history-service/internal/models"
)

// hydrateReactions populates msgs[i].Reactions in place from the side table.
func (s *HistoryService) hydrateReactions(ctx context.Context, msgs []models.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	ids := make([]string, len(msgs))
	for i := range msgs {
		ids[i] = msgs[i].MessageID
	}
	reactions, err := s.msgReader.GetReactionsByMessageIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("hydrating reactions: %w", err)
	}
	for i := range msgs {
		if r, ok := reactions[msgs[i].MessageID]; ok {
			msgs[i].Reactions = r
		}
	}
	return nil
}
