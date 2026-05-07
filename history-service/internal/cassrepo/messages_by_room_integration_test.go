//go:build integration

package cassrepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/pkg/atrest"
	cassmodel "github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/testutil"
)

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	return testutil.MongoDB(t, "history_cassrepo_test")
}

func writeTestKEKFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "keks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
        "current": 1,
        "keys": {"1": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}
    }`), 0o600))
	return path
}

func seedMessages(t *testing.T, session *gocql.Session, roomID string, base time.Time, count int) {
	t.Helper()
	sender := models.Participant{ID: "u1", Account: "user1"}
	for i := 0; i < count; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		err := session.Query(
			`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg) VALUES (?, ?, ?, ?, ?)`,
			roomID, ts, fmt.Sprintf("m%d", i), sender, fmt.Sprintf("msg-%d", i),
		).Exec()
		require.NoError(t, err)
	}
}

func TestRepository_GetMessagesBefore(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session, nil)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParsePageRequest("", 3)
	require.NoError(t, err)

	page, err := repo.GetMessagesBefore(ctx, "r1", base.Add(10*time.Minute), q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 3)
	assert.True(t, page.Data[0].CreatedAt.After(page.Data[1].CreatedAt))
}

func TestRepository_GetMessagesBetweenDesc(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session, nil)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetMessagesBetweenDesc(ctx, "r1", base.Add(1*time.Minute), base.Add(4*time.Minute), q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 2)                                          // m2 (2min), m3 (3min) — excludes 1min and 4min
	assert.True(t, page.Data[0].CreatedAt.After(page.Data[1].CreatedAt)) // DESC order
}

func TestRepository_GetMessagesAfter(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session, nil)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetMessagesAfter(ctx, "r1", base.Add(2*time.Minute), q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 2)                                           // m3 (3min), m4 (4min) — strictly after 2min
	assert.True(t, page.Data[0].CreatedAt.Before(page.Data[1].CreatedAt)) // ASC order
}

func TestRepository_GetAllMessagesAsc(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session, nil)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParsePageRequest("", 3)
	require.NoError(t, err)

	page, err := repo.GetAllMessagesAsc(ctx, "r1", q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 3)
	assert.True(t, page.Data[0].CreatedAt.Before(page.Data[1].CreatedAt)) // ASC order
	assert.True(t, page.HasNext)
}

func TestHybridRead_LegacyAndEncrypted(t *testing.T) {
	ctx := context.Background()
	session := setupCassandra(t)
	mongoDB := setupMongo(t)

	loader, err := atrest.NewFileKEKLoader(writeTestKEKFile(t), 0)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Best-effort: tests can't meaningfully act on a Close failure.
		_ = loader.Close()
	})

	cipher := atrest.NewCipher(loader, atrest.NewMongoDEKStore(mongoDB.Collection(atrest.CollectionName)),
		atrest.Config{DEKCacheSize: 100, DEKCacheTTL: time.Hour})
	repo := NewRepository(session, cipher)

	now := time.Now().UTC().Truncate(time.Millisecond)
	roomID := "r-hybrid-1"

	// Insert a legacy plaintext row directly with CQL.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, msg, site_id) VALUES (?, ?, ?, ?, ?)`,
		roomID, now, "legacy", "plaintext-body", "site-a",
	).Exec())

	// Insert an encrypted row by going through the cipher.
	enc := atrest.EncryptedFields{Msg: "encrypted-body"}
	payload, meta, err := cipher.Encrypt(ctx, roomID, enc)
	require.NoError(t, err)
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, enc_payload, enc_meta, site_id) VALUES (?, ?, ?, ?, ?, ?)`,
		roomID, now.Add(time.Second), "encrypted", payload, &cassmodel.EncMeta{Nonce: meta.Nonce}, "site-a",
	).Exec())

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetAllMessagesAsc(ctx, roomID, q)
	require.NoError(t, err)
	require.Len(t, page.Data, 2)

	bodies := []string{page.Data[0].Msg, page.Data[1].Msg}
	assert.Contains(t, bodies, "plaintext-body")
	assert.Contains(t, bodies, "encrypted-body")

	// Encrypted row should NOT leak enc_* fields above the repo layer.
	for _, m := range page.Data {
		assert.Empty(t, m.EncPayload)
		assert.Nil(t, m.EncMeta)
	}
}

func TestRepository_GetMessagesBefore_ThreadRoomID(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session, nil)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "user1"}
	ts := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_room_id) VALUES (?, ?, ?, ?, ?, ?)`,
		"r-thread", ts, "m-thread", sender, "reply", "tr-42",
	).Exec())

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetMessagesBefore(ctx, "r-thread", ts.Add(1*time.Minute), q)
	require.NoError(t, err)
	require.Len(t, page.Data, 1)
	assert.Equal(t, "tr-42", page.Data[0].ThreadRoomID)
}
