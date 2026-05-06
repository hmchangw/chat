package cassrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/history-service/internal/models"
)

const baseColumns = "room_id, created_at, message_id, thread_room_id, sender, target_user, " +
	"msg, mentions, attachments, file, card, card_action, tshow, tcount, " +
	"thread_parent_id, thread_parent_created_at, quoted_parent_message, " +
	"visible_to, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"

const messageByRoomQuery = "SELECT " + baseColumns + " FROM messages_by_room"

// scanMsgsFromIter collects all rows from iter into a slice.
// structScan ignores columns absent from the struct's cql tags, so this
// helper is safe to use with any column subset (e.g. messageByIDQuery
// includes pinned_at/pinned_by which are absent from the base column list).
func scanMsgsFromIter(iter *gocql.Iter) []models.Message {
	messages := make([]models.Message, 0)
	for {
		var m models.Message
		if !structScan(iter, &m) {
			break
		}
		messages = append(messages, m)
	}
	return messages
}

// startBucketFromCursor returns the bucket to start the walk at, plus an
// initial in-bucket pageState if the request carried a non-empty cursor.
// When the cursor is empty, defaultBucket is used.
func startBucketFromCursor(pageReq PageRequest, defaultBucket int64) (int64, []byte, error) {
	if pageReq.Cursor == nil {
		return defaultBucket, nil, nil
	}
	encoded := pageReq.Cursor.Encode()
	if encoded == "" {
		return defaultBucket, nil, nil
	}
	bucket, pageState, err := decodeBucketCursor(encoded)
	if err != nil {
		return 0, nil, fmt.Errorf("start bucket from cursor: %w", err)
	}
	return bucket, pageState, nil
}

// scanMessagesUpTo consumes up to remaining rows from iter via structScan.
func scanMessagesUpTo(iter *gocql.Iter, remaining int) []models.Message {
	out := make([]models.Message, 0, remaining)
	for len(out) < remaining {
		var m models.Message
		if !structScan(iter, &m) {
			break
		}
		out = append(out, m)
	}
	return out
}

func (r *Repository) GetMessagesBefore(ctx context.Context, roomID string, before time.Time, floor time.Time, pageReq PageRequest) (Page[models.Message], error) {
	startBucket, initialPageState, err := startBucketFromCursor(pageReq, r.bucket.Of(before))
	if err != nil {
		return Page[models.Message]{}, err
	}
	floorBucket := r.bucket.Of(floor)

	queryFn := func(bucket int64, firstBucket bool) *gocql.Query {
		if firstBucket {
			return r.session.Query(
				messageByRoomQuery+` WHERE room_id = ? AND bucket = ? AND created_at < ? ORDER BY created_at DESC`,
				roomID, bucket, before,
			)
		}
		return r.session.Query(
			messageByRoomQuery+` WHERE room_id = ? AND bucket = ? ORDER BY created_at DESC`,
			roomID, bucket,
		)
	}

	res, err := fillPage[models.Message](
		ctx, r.bucket, walkDesc, startBucket, floorBucket, r.maxBuckets,
		pageReq.PageSize, initialPageState, queryFn, scanMessagesUpTo,
	)
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("get messages before: %w", err)
	}
	return Page[models.Message]{Data: res.Rows, NextCursor: res.NextCursor, HasNext: res.HasNext}, nil
}

func (r *Repository) GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, pageReq PageRequest) (Page[models.Message], error) {
	startBucket, initialPageState, err := startBucketFromCursor(pageReq, r.bucket.Of(before))
	if err != nil {
		return Page[models.Message]{}, err
	}
	floorBucket := r.bucket.Of(since)

	queryFn := func(bucket int64, firstBucket bool) *gocql.Query {
		if firstBucket {
			return r.session.Query(
				messageByRoomQuery+` WHERE room_id = ? AND bucket = ? AND created_at > ? AND created_at < ? ORDER BY created_at DESC`,
				roomID, bucket, since, before,
			)
		}
		return r.session.Query(
			messageByRoomQuery+` WHERE room_id = ? AND bucket = ? ORDER BY created_at DESC`,
			roomID, bucket,
		)
	}

	res, err := fillPage[models.Message](
		ctx, r.bucket, walkDesc, startBucket, floorBucket, r.maxBuckets,
		pageReq.PageSize, initialPageState, queryFn, scanMessagesUpTo,
	)
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("get messages between desc: %w", err)
	}
	return Page[models.Message]{Data: res.Rows, NextCursor: res.NextCursor, HasNext: res.HasNext}, nil
}

func (r *Repository) GetMessagesAfter(ctx context.Context, roomID string, after time.Time, ceiling time.Time, pageReq PageRequest) (Page[models.Message], error) {
	startBucket, initialPageState, err := startBucketFromCursor(pageReq, r.bucket.Of(after))
	if err != nil {
		return Page[models.Message]{}, err
	}
	ceilingBucket := r.bucket.Of(ceiling)

	queryFn := func(bucket int64, firstBucket bool) *gocql.Query {
		if firstBucket {
			return r.session.Query(
				messageByRoomQuery+` WHERE room_id = ? AND bucket = ? AND created_at > ? ORDER BY created_at ASC`,
				roomID, bucket, after,
			)
		}
		return r.session.Query(
			messageByRoomQuery+` WHERE room_id = ? AND bucket = ? ORDER BY created_at ASC`,
			roomID, bucket,
		)
	}

	res, err := fillPage[models.Message](
		ctx, r.bucket, walkAsc, startBucket, ceilingBucket, r.maxBuckets,
		pageReq.PageSize, initialPageState, queryFn, scanMessagesUpTo,
	)
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("get messages after: %w", err)
	}
	return Page[models.Message]{Data: res.Rows, NextCursor: res.NextCursor, HasNext: res.HasNext}, nil
}

func (r *Repository) GetAllMessagesAsc(ctx context.Context, roomID string, floor time.Time, ceiling time.Time, pageReq PageRequest) (Page[models.Message], error) {
	startBucket, initialPageState, err := startBucketFromCursor(pageReq, r.bucket.Of(floor))
	if err != nil {
		return Page[models.Message]{}, err
	}
	ceilingBucket := r.bucket.Of(ceiling)

	queryFn := func(bucket int64, _ bool) *gocql.Query {
		return r.session.Query(
			messageByRoomQuery+` WHERE room_id = ? AND bucket = ? ORDER BY created_at ASC`,
			roomID, bucket,
		)
	}

	res, err := fillPage[models.Message](
		ctx, r.bucket, walkAsc, startBucket, ceilingBucket, r.maxBuckets,
		pageReq.PageSize, initialPageState, queryFn, scanMessagesUpTo,
	)
	if err != nil {
		return Page[models.Message]{}, fmt.Errorf("get all messages asc: %w", err)
	}
	return Page[models.Message]{Data: res.Rows, NextCursor: res.NextCursor, HasNext: res.HasNext}, nil
}
