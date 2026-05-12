//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/gocql/gocql"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// mongoOptionsReplaceUpsert returns ReplaceOptions with Upsert(true). Wrapped
// in a helper because the v2 driver's API is verbose at the call site.
func mongoOptionsReplaceUpsert() *options.ReplaceOptionsBuilder {
	return options.Replace().SetUpsert(true)
}

// SystemConn returns a NATS connection authenticated with backend.creds (the
// all-services credential). Used by the harness for negative-observation and
// stack-inspection tasks where per-user JWT auth isn't required. For tests
// acting AS a user, use Authenticate(...).Conn().
func (s SiteEndpoints) SystemConn(t *testing.T) *nats.Conn {
	t.Helper()
	conn, err := nats.Connect(s.NATSURL, nats.UserCredentials(s.NATSCredsFile))
	require.NoError(t, err, "nats system-creds connect %s", s.SiteID)
	t.Cleanup(conn.Close)
	return conn
}

// JetStream returns a JetStream context bound to a fresh system-creds NATS
// connection. Suitable for stream / consumer inspection from tests; per-user
// JetStream interactions should use Authenticate(...).Conn() with a derived
// jetstream.New().
func (s SiteEndpoints) JetStream(t *testing.T) jetstream.JetStream {
	t.Helper()
	js, err := jetstream.New(s.SystemConn(t))
	require.NoError(t, err)
	return js
}

// MongoDB returns the chat database on the site's mongo. Use raw collections;
// tests are free to inspect or seed state for integration scenarios.
func (s SiteEndpoints) MongoDB(t *testing.T) *mongo.Database {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := mongo.Connect(options.Client().ApplyURI(s.MongoURI))
	require.NoError(t, err, "mongo connect %s", s.SiteID)
	// Verify reachability up front so a misconfigured URI fails here, not on
	// first query.
	require.NoError(t, c.Ping(ctx, nil), "mongo ping %s", s.SiteID)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = c.Disconnect(stopCtx)
	})
	return c.Database("chat")
}

// CassandraSession returns an open gocql session on the site's keyspace.
// CassandraHosts may carry "host:port" pairs (e.g. "localhost:19042"); gocql
// expects the host list to be just hosts plus a separate Port field, so split
// here.
func (s SiteEndpoints) CassandraSession(t *testing.T) *gocql.Session {
	t.Helper()
	cluster := gocql.NewCluster()
	for _, hp := range s.CassandraHosts {
		host, portStr, hasPort := strings.Cut(hp, ":")
		cluster.Hosts = append(cluster.Hosts, host)
		if hasPort && portStr != "" {
			port, err := strconv.Atoi(portStr)
			require.NoError(t, err, "parse cassandra port %q", hp)
			cluster.Port = port
		}
	}
	cluster.Keyspace = "chat"
	cluster.Consistency = gocql.LocalQuorum
	cluster.Timeout = 10 * time.Second
	sess, err := cluster.CreateSession()
	require.NoError(t, err, "cassandra connect %s", s.SiteID)
	t.Cleanup(sess.Close)
	return sess
}

// HTTPClient returns a Resty client preconfigured to talk to this site's
// auth-service. Tests use it for the /auth endpoint and any other HTTP
// surface area introduced later.
func (s SiteEndpoints) HTTPClient(t *testing.T) *resty.Client {
	t.Helper()
	return resty.New().
		SetBaseURL(s.AuthURL).
		SetTimeout(15*time.Second).
		SetHeader("Accept", "application/json")
}

// ESClient returns a minimal http.Client + base URL for direct ES queries.
// search-service tests use it to assert documents are present in the
// expected per-site index.
func (s SiteEndpoints) ESClient(t *testing.T) (*http.Client, string) {
	t.Helper()
	return http.DefaultClient, s.ESURL
}

// SeedRemoteUser inserts a stub User document into the site's users
// originating site (where the inviter lives) to know that the invitee
// belongs to a different site, so room-worker's invite path can publish to
// the correct outbox.{src}.to.{dst}.member_added subject. Production has a
// separate user-replication mechanism out of scope here; the test simulates
// it directly.
//
// Idempotent: re-inserts with the same _id replace the prior document.
func (s SiteEndpoints) SeedRemoteUser(t *testing.T, ctx context.Context, account, siteID string) {
	t.Helper()
	db := s.MongoDB(t)
	users := db.Collection("users")
	// Match the pkg/model.User shape; _id and Account are required for
	// room-worker's lookups; the other fields are cosmetic and left empty.
	doc := bson.M{
		"_id":         account,
		"account":     account,
		"siteId":      siteID,
		"engName":     account, // satisfies "missing required name fields"
		"chineseName": account,
		"sectId":      "test",
		"sectName":    "test",
		"employeeId":  account,
	}
	_, err := users.ReplaceOne(ctx, bson.M{"_id": account}, doc,
		mongoOptionsReplaceUpsert())
	require.NoError(t, err, "seed remote user %s on %s", account, s.SiteID)
}

// SeedUserRoom pre-creates a user-room ES doc so search-service's restricted-
// rooms machinery permits the user to search the given rooms. Production
// search-sync-worker populates this from cross-site INBOX events; the doc
// remains empty for purely-local users on a single-site stack, blocking
// search-service queries.
//
// Idempotent: PUTting the same _id overwrites the document. roomIDs is the
// set of room IDs the user should be considered a regular (non-restricted)
// member of. The doc's `roomTimestamps` is populated with `now` for each
// room so any subsequent script-update by search-sync-worker that compares
// timestamps does the right last-write-wins.
//
// Index name is hardcoded to `user-room-{siteID-lowercased}` because ES
// rejects uppercase characters in index names (live-debug finding) and
// search-service / search-sync-worker both auto-derive to this form when
// USER_ROOM_INDEX is empty.
func (s SiteEndpoints) SeedUserRoom(t *testing.T, account string, roomIDs []string) {
	t.Helper()
	now := time.Now().UnixMilli()
	timestamps := make(map[string]int64, len(roomIDs))
	for _, rid := range roomIDs {
		timestamps[rid] = now
	}
	body, err := json.Marshal(map[string]any{
		"account":         account,
		"siteId":          strings.ToLower(s.SiteID),
		"rooms":           roomIDs,
		"restrictedRooms": map[string]any{}, // empty = no restrictions
		"roomTimestamps":  timestamps,
		"updatedAt":       now,
	})
	require.NoError(t, err)

	indexName := "user-room-" + strings.ToLower(s.SiteID)
	url := s.ESURL + "/" + indexName + "/_doc/" + account + "?refresh=true"
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "seed user-room doc")
	defer resp.Body.Close()
	require.LessOrEqual(t, resp.StatusCode, 299,
		"seed user-room: status %d", resp.StatusCode)
}

// FlushSearchRestrictedCache deletes search-service's per-account
// restricted-rooms entry from this site's Valkey so the next search-service
// call falls through to the ES user-room doc (where the test's SeedUserRoom
// just wrote the truth). Without this, a stale cached entry from a prior run
// — or one populated by search-service itself on a previous request with no
// rooms — can return 0 results despite a freshly-seeded user-room doc.
//
// Key format mirrors search-service/store_valkey.go restrictedKey:
//
//	searchservice:restrictedrooms:{account}
//
// DEL is idempotent — calling on an absent key is a no-op.
func (s SiteEndpoints) FlushSearchRestrictedCache(t *testing.T, account string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := redis.NewClient(&redis.Options{Addr: s.ValkeyAddr})
	defer func() { _ = client.Close() }()

	key := fmt.Sprintf("searchservice:restrictedrooms:%s", account)
	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatalf("flush search restricted cache key=%s: %v", key, err)
	}
}

// SeedRoomKey writes a room encryption key pair into this site's Valkey so
// broadcast-worker (running with ENCRYPTION_ENABLED=true) can pick it up
// when it processes a message for the room. Uses the production
// roomkeystore.NewValkeyStore + Set path so this stays in lockstep with the
// real wire format (hash key `room:{roomID}:key`, base64-encoded `pub` +
// `priv` fields, version=0 on first Set).
//
// Per-call connection: cheap, called once per encryption test. The
// underlying go-redis client closes via defer so there's no leak across
// repeated test runs.
func (s SiteEndpoints) SeedRoomKey(t *testing.T, roomID string, pair roomkeystore.RoomKeyPair) {
	t.Helper()
	store, err := roomkeystore.NewValkeyStore(roomkeystore.Config{
		Addr:        s.ValkeyAddr,
		GracePeriod: 24 * time.Hour, // unused by Set, required by Config
	})
	require.NoError(t, err, "open valkey for SeedRoomKey")
	defer func() { _ = store.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := store.Set(ctx, roomID, pair); err != nil {
		t.Fatalf("seed room key for %s: %v", roomID, err)
	}
}

// DeleteRoomKey removes a room key from this site's Valkey. Best-effort
// cleanup -- a leftover key is harmless across runs (rooms are unique per
// test) but tidy teardown helps when grepping live valkey state during
// debugging.
func (s SiteEndpoints) DeleteRoomKey(t *testing.T, roomID string) {
	t.Helper()
	store, err := roomkeystore.NewValkeyStore(roomkeystore.Config{
		Addr:        s.ValkeyAddr,
		GracePeriod: 24 * time.Hour,
	})
	if err != nil {
		t.Logf("DeleteRoomKey open valkey: %v (ignoring)", err)
		return
	}
	defer func() { _ = store.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Delete(ctx, roomID); err != nil {
		t.Logf("DeleteRoomKey for %s: %v (ignoring)", roomID, err)
	}
}
