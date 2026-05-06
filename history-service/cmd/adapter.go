package main

// repoAdapter is a transient compile shim. Remove when Task 15 updates
// service.MessageReader to the new cassrepo bucket-walk signatures and
// Task 17 supplies real room-time bounds.
//
// It wraps *cassrepo.Repository and adapts the new method signatures (which
// carry floor/ceiling time.Time parameters) down to the old
// service.MessageRepository interface (which has no bounds parameters).
// All placeholder bounds carry TODO(task-17) markers.

import (
	"context"
	"time"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
)

type repoAdapter struct{ r *cassrepo.Repository }

func newRepoAdapter(r *cassrepo.Repository) *repoAdapter { return &repoAdapter{r: r} }

// ---- MessageWriter (unchanged, forward directly) ----

func (a *repoAdapter) UpdateMessageContent(ctx context.Context, msg *models.Message, newMsg string, editedAt time.Time) error {
	return a.r.UpdateMessageContent(ctx, msg, newMsg, editedAt)
}

func (a *repoAdapter) SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) (time.Time, bool, error) {
	return a.r.SoftDeleteMessage(ctx, msg, deletedAt)
}

// ---- MessageReader (old interface, shims to new cassrepo signatures) ----

func (a *repoAdapter) GetMessagesBefore(ctx context.Context, roomID string, before time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error) {
	// TODO(task-17): replace time.Time{} with resolved room floor from resolveRoomTimes.
	return a.r.GetMessagesBefore(ctx, roomID, before, time.Time{}, pageReq)
}

func (a *repoAdapter) GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error) {
	return a.r.GetMessagesBetweenDesc(ctx, roomID, since, before, pageReq)
}

func (a *repoAdapter) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error) {
	// TODO(task-17): replace time.Now().UTC().Add(time.Hour) with resolved room ceiling from resolveRoomTimes.
	return a.r.GetMessagesAfter(ctx, roomID, after, time.Now().UTC().Add(time.Hour), pageReq)
}

func (a *repoAdapter) GetAllMessagesAsc(ctx context.Context, roomID string, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error) {
	// TODO(task-17): replace placeholders with resolved room times from resolveRoomTimes.
	return a.r.GetAllMessagesAsc(ctx, roomID, time.Time{}, time.Now().UTC().Add(time.Hour), pageReq)
}

func (a *repoAdapter) GetMessageByID(ctx context.Context, messageID string) (*models.Message, error) {
	return a.r.GetMessageByID(ctx, messageID)
}

func (a *repoAdapter) GetThreadMessages(ctx context.Context, roomID, threadRoomID string, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error) {
	// TODO(task-17): replace with resolved room times from resolveRoomTimes.
	return a.r.GetThreadMessages(ctx, roomID, threadRoomID, time.Now().UTC().Add(time.Hour), time.Time{}, pageReq)
}

func (a *repoAdapter) GetMessagesByIDs(ctx context.Context, messageIDs []string) ([]models.Message, error) {
	return a.r.GetMessagesByIDs(ctx, messageIDs)
}
