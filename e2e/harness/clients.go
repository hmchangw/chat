//go:build e2e

package harness

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/gocql/gocql"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

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
		SetTimeout(15 * time.Second).
		SetHeader("Accept", "application/json")
}

// ESClient returns a minimal http.Client + base URL for direct ES queries.
// search-service tests use it to assert documents are present in the
// expected per-site index.
func (s SiteEndpoints) ESClient(t *testing.T) (*http.Client, string) {
	t.Helper()
	return http.DefaultClient, s.ESURL
}
