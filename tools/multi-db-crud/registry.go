package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// connection holds either a Mongo client or a Cassandra session, keyed by
// kind. Exactly one of mongo/cass is non-nil.
type connection struct {
	kind         string         // "mongo" | "cassandra"
	mongo        *mongo.Client  // nil for cassandra
	cass         *gocql.Session // nil for mongo
	cassKeyspace string         // cassandra only
	label        string         // user-provided nickname
	createdAt    time.Time
	lastUsed     atomic.Int64 // unix ms

	// serverVersion is fetched best-effort at Connect time and surfaced via List.
	serverVersion string
}

// registry is a thread-safe pool of database connections keyed by opaque
// UUID. It supports idle reaping via Reap (typically run on a ticker).
type registry struct {
	mu          sync.RWMutex
	conns       map[string]*connection
	idleTimeout time.Duration
	now         func() time.Time

	// Dependency-injected connect/close functions — enables testing without real DBs.
	mongoConnect func(ctx context.Context, uri string) (*mongo.Client, error)
	cassConnect  func(hosts []string, keyspace string) (*gocql.Session, error)
	closeMongo   func(ctx context.Context, c *mongo.Client) error
	closeCass    func(s *gocql.Session)
}

// connectSpec is the input shape for Connect.
type connectSpec struct {
	Kind     string // "mongo" | "cassandra"
	URI      string // for mongo: full URI; for cassandra: comma-separated hosts
	Keyspace string // cassandra only
	Label    string // user-provided nickname
}

// connInfo is returned metadata (no URIs).
type connInfo struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Label         string    `json:"label"`
	Keyspace      string    `json:"keyspace,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	ServerVersion string    `json:"serverVersion,omitempty"`
}

// Sentinel errors.
var (
	ErrNotFound    = errors.New("connection not found")
	ErrUnknownKind = errors.New("unknown connection kind")
)

// defaultCloseMongo disconnects a *mongo.Client. Default close hook used by newRegistry.
func defaultCloseMongo(ctx context.Context, c *mongo.Client) error { return c.Disconnect(ctx) }

// defaultCloseCass closes a *gocql.Session. Default close hook used by newRegistry.
func defaultCloseCass(s *gocql.Session) { s.Close() }

// defaultMongoConnect dials Mongo using the v2 driver and pings it.
// We deliberately do not use pkg/mongoutil because it logs the URI.
func defaultMongoConnect(ctx context.Context, uri string) (*mongo.Client, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("dial mongo: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		// Disconnect the partially constructed client to avoid leaks.
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("ping mongo: %w", err)
	}
	return client, nil
}

// defaultCassConnect dials Cassandra without logging the keyspace or hosts.
func defaultCassConnect(hosts []string, keyspace string) (*gocql.Session, error) {
	cluster := gocql.NewCluster(hosts...)
	cluster.Keyspace = keyspace
	cluster.Consistency = gocql.LocalQuorum
	cluster.Timeout = 10 * time.Second
	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("dial cassandra: %w", err)
	}
	return session, nil
}

// newRegistry constructs a registry with default connect functions.
func newRegistry(idleTimeout time.Duration) *registry {
	return &registry{
		conns:        make(map[string]*connection),
		idleTimeout:  idleTimeout,
		now:          time.Now,
		mongoConnect: defaultMongoConnect,
		cassConnect:  defaultCassConnect,
		closeMongo:   defaultCloseMongo,
		closeCass:    defaultCloseCass,
	}
}

// Connect opens a new connection of the requested kind, stores it in the
// registry, and returns metadata. Returns ErrUnknownKind for unsupported
// kinds and a wrapped driver error on connect failure.
func (r *registry) Connect(ctx context.Context, spec connectSpec) (connInfo, error) {
	switch spec.Kind {
	case "mongo":
		return r.connectMongo(ctx, spec)
	case "cassandra":
		return r.connectCassandra(spec)
	default:
		return connInfo{}, ErrUnknownKind
	}
}

func (r *registry) connectMongo(ctx context.Context, spec connectSpec) (connInfo, error) {
	client, err := r.mongoConnect(ctx, spec.URI)
	if err != nil {
		return connInfo{}, fmt.Errorf("could not connect to %s: %w", spec.Kind, err)
	}

	// Best-effort server version lookup. Errors are ignored on purpose so
	// that connections still succeed against servers that disallow buildInfo.
	version := mongoServerVersion(ctx, client)

	id := uuid.NewString()
	now := r.now()
	c := &connection{
		kind:          "mongo",
		mongo:         client,
		label:         spec.Label,
		createdAt:     now,
		serverVersion: version,
	}
	c.lastUsed.Store(now.UnixMilli())

	r.mu.Lock()
	r.conns[id] = c
	r.mu.Unlock()

	slog.Info("registered connection",
		"id", id,
		"kind", "mongo",
		"label", spec.Label,
	)

	return connInfo{
		ID:            id,
		Kind:          "mongo",
		Label:         spec.Label,
		CreatedAt:     now,
		ServerVersion: version,
	}, nil
}

func (r *registry) connectCassandra(spec connectSpec) (connInfo, error) {
	hosts := splitAndTrim(spec.URI)

	session, err := r.cassConnect(hosts, spec.Keyspace)
	if err != nil {
		return connInfo{}, fmt.Errorf("could not connect to %s: %w", spec.Kind, err)
	}

	version := cassServerVersion(session)

	id := uuid.NewString()
	now := r.now()
	c := &connection{
		kind:          "cassandra",
		cass:          session,
		cassKeyspace:  spec.Keyspace,
		label:         spec.Label,
		createdAt:     now,
		serverVersion: version,
	}
	c.lastUsed.Store(now.UnixMilli())

	r.mu.Lock()
	r.conns[id] = c
	r.mu.Unlock()

	slog.Info("registered connection",
		"id", id,
		"kind", "cassandra",
		"label", spec.Label,
		"keyspace", spec.Keyspace,
	)

	return connInfo{
		ID:            id,
		Kind:          "cassandra",
		Label:         spec.Label,
		Keyspace:      spec.Keyspace,
		CreatedAt:     now,
		ServerVersion: version,
	}, nil
}

// splitAndTrim splits a comma-separated host list and trims whitespace from each entry.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// mongoServerVersion runs buildInfo against admin and returns the version
// string, or empty on any failure. Bounded by a short timeout so a dead
// server does not block Connect for the driver's default SDAM window.
func mongoServerVersion(ctx context.Context, client *mongo.Client) string {
	if client == nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res := client.Database("admin").RunCommand(cctx, bson.D{{Key: "buildInfo", Value: 1}})
	if res == nil || res.Err() != nil {
		return ""
	}
	var raw bson.M
	if err := res.Decode(&raw); err != nil {
		return ""
	}
	if v, ok := raw["version"].(string); ok {
		return v
	}
	return ""
}

// cassServerVersion queries system.local for the release_version, or empty on failure.
func cassServerVersion(session *gocql.Session) string {
	if session == nil {
		return ""
	}
	var v string
	if err := session.Query("SELECT release_version FROM system.local").Scan(&v); err != nil {
		return ""
	}
	return v
}

// Get retrieves a connection by ID, touching its lastUsed timestamp.
// Returns ErrNotFound if no such connection exists.
func (r *registry) Get(id string) (*connection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.conns[id]
	if !ok {
		return nil, ErrNotFound
	}
	c.lastUsed.Store(r.now().UnixMilli())
	return c, nil
}

// Close removes the connection from the registry and shuts down its
// underlying client. Returns ErrNotFound if no such connection exists.
func (r *registry) Close(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.conns[id]
	if !ok {
		return ErrNotFound
	}
	delete(r.conns, id)
	r.closeConnection(c)
	return nil
}

// closeConnection invokes the appropriate driver close hook for c. Errors
// are logged but not returned because callers cannot meaningfully act on
// driver-close failures.
func (r *registry) closeConnection(c *connection) {
	switch c.kind {
	case "mongo":
		if c.mongo != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.closeMongo(ctx, c.mongo); err != nil {
				slog.Warn("mongo close failed", "error", err, "label", c.label)
			}
		}
	case "cassandra":
		if c.cass != nil {
			r.closeCass(c.cass)
		}
	}
}

// List returns metadata for all registered connections sorted by createdAt ascending.
func (r *registry) List() []connInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]connInfo, 0, len(r.conns))
	for id, c := range r.conns {
		out = append(out, connInfo{
			ID:            id,
			Kind:          c.kind,
			Label:         c.label,
			Keyspace:      c.cassKeyspace,
			CreatedAt:     c.createdAt,
			ServerVersion: c.serverVersion,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// CloseAll closes every connection in the registry and clears the map.
func (r *registry) CloseAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.conns {
		r.closeConnection(c)
		delete(r.conns, id)
	}
}

// Reap evicts and closes any connection whose lastUsed is older than
// idleTimeout. Intended to be called periodically from a background goroutine.
func (r *registry) Reap() {
	r.mu.Lock()
	defer r.mu.Unlock()
	thresholdMs := r.idleTimeout.Milliseconds()
	nowMs := r.now().UnixMilli()
	for id, c := range r.conns {
		if nowMs-c.lastUsed.Load() > thresholdMs {
			slog.Info("reaped idle connection",
				"id", id,
				"label", c.label,
				"kind", c.kind,
			)
			r.closeConnection(c)
			delete(r.conns, id)
		}
	}
}

// runReaper drives reg.Reap on a ticker until ctx is cancelled.
func runReaper(ctx context.Context, reg *registry, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reg.Reap()
		}
	}
}
