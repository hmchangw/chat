# Optional MongoDB and Cassandra Credentials

## Goal

Allow every microservice that connects to MongoDB or Cassandra to optionally authenticate with a username and password supplied via environment variables. The change must be backwards compatible — services configured without credentials continue to connect anonymously exactly as they do today.

## Non-Goals

- No changes to `docker-local/` Docker Compose files.
- No new authentication knobs beyond username and password (no `authSource`, no TLS toggles, no SASL mechanisms).
- No changes to services that do not connect to MongoDB or Cassandra (`auth-service`, `chat-frontend`, `search-sync-worker`).
- No changes to existing integration tests — empty credentials must keep them passing against the current non-auth testcontainer setups.

## Environment Variables

Four new variables, all optional, all defaulting to the empty string:

| Variable | Used by |
|---|---|
| `MONGO_USERNAME` | every Mongo-connecting service + `tools/loadgen` |
| `MONGO_PASSWORD` | every Mongo-connecting service + `tools/loadgen` |
| `CASSANDRA_USERNAME` | `message-worker`, `history-service` |
| `CASSANDRA_PASSWORD` | `message-worker`, `history-service` |

Semantics: when both username and password are non-empty, authenticate. Otherwise, connect without auth (today's behavior).

## Helper Changes

### `pkg/mongoutil/mongo.go`

New signature:

```go
func Connect(ctx context.Context, uri, username, password string) (*mongo.Client, error)
```

When `username != "" && password != ""`, build the client options as:

```go
options.Client().ApplyURI(uri).SetAuth(options.Credential{
    Username: username,
    Password: password,
})
```

Otherwise, use `options.Client().ApplyURI(uri)` exactly as today. The existing `slog.Info("connected to MongoDB", "uri", uri)` line is unchanged — never log the password.

### `pkg/cassutil/cass.go`

New signature:

```go
func Connect(hosts []string, keyspace, username, password string) (*gocql.Session, error)
```

When both credentials are non-empty:

```go
cluster.Authenticator = gocql.PasswordAuthenticator{
    Username: username,
    Password: password,
}
```

Otherwise, no authenticator is set — today's behavior.

## Service Configuration Changes

### Flat-config services

Add to each service's `Config` struct:

```go
MongoUsername string `env:"MONGO_USERNAME" envDefault:""`
MongoPassword string `env:"MONGO_PASSWORD" envDefault:""`
```

Affected services:
- `room-service`
- `message-gatekeeper`
- `broadcast-worker`
- `message-worker`
- `notification-worker`
- `inbox-worker`
- `room-worker`
- `tools/loadgen`

Additionally, `message-worker` gets:

```go
CassandraUsername string `env:"CASSANDRA_USERNAME" envDefault:""`
CassandraPassword string `env:"CASSANDRA_PASSWORD" envDefault:""`
```

### `history-service` (nested config)

In `history-service/internal/config/config.go`, extend the existing nested structs:

```go
type MongoConfig struct {
    URI      string `env:"URI" required:"true"`
    DB       string `env:"DB"  envDefault:"chat"`
    Username string `env:"USERNAME" envDefault:""`
    Password string `env:"PASSWORD" envDefault:""`
}

type CassandraConfig struct {
    Hosts    string `env:"HOSTS"    required:"true"`
    Keyspace string `env:"KEYSPACE" envDefault:"chat"`
    Username string `env:"USERNAME" envDefault:""`
    Password string `env:"PASSWORD" envDefault:""`
}
```

The existing `envPrefix:"MONGO_"` and `envPrefix:"CASSANDRA_"` on the parent `Config` produce the same final variable names (`MONGO_USERNAME`, `CASSANDRA_USERNAME`, etc.).

## Callsite Updates

Every existing `mongoutil.Connect(ctx, uri)` callsite becomes:

```go
mongoutil.Connect(ctx, cfg.MongoURI, cfg.MongoUsername, cfg.MongoPassword)
```

Or for `history-service`:

```go
mongoutil.Connect(ctx, cfg.Mongo.URI, cfg.Mongo.Username, cfg.Mongo.Password)
```

Every existing `cassutil.Connect(hosts, keyspace)` callsite becomes:

```go
cassutil.Connect(strings.Split(cfg.CassandraHosts, ","), cfg.CassandraKeyspace,
    cfg.CassandraUsername, cfg.CassandraPassword)
```

Or for `history-service`:

```go
cassutil.Connect(strings.Split(cfg.Cassandra.Hosts, ","), cfg.Cassandra.Keyspace,
    cfg.Cassandra.Username, cfg.Cassandra.Password)
```

Total: 9 callsite updates (8 Mongo + 2 Cassandra; `tools/loadgen` has 2 Mongo callsites).

## Testing

Per `CLAUDE.md` TDD requirements, follow Red-Green-Refactor with extracted pure helpers so the credential-branching logic can be unit-tested without containers.

### `pkg/mongoutil`

Extract a pure helper:

```go
func buildClientOptions(uri, username, password string) *options.ClientOptions
```

`Connect` calls this helper. New `mongoutil_test.go` table-driven test covers:
- empty username and empty password → no `Auth` set
- empty username, non-empty password → no `Auth` set
- non-empty username, empty password → no `Auth` set
- both non-empty → `Auth.Username` and `Auth.Password` set correctly

The test asserts on the `*options.ClientOptions` returned, never connects to a database.

### `pkg/cassutil`

Extract a pure helper:

```go
func buildCluster(hosts []string, keyspace, username, password string) *gocql.ClusterConfig
```

`Connect` calls this helper. New `cassutil_test.go` table-driven test covers the same four credential combinations and asserts on `cluster.Authenticator` (nil vs `gocql.PasswordAuthenticator`).

### Existing tests

Existing integration tests pass empty strings for the new credential parameters and continue to run unchanged against today's testcontainer setups.

## Out of Scope (Confirmed)

- Docker Compose files in `docker-local/`.
- Auth-enabled testcontainer integration tests.
- New env vars beyond the four listed above.
- Changes to services that do not use MongoDB or Cassandra.

## Implementation Order

1. Update `pkg/mongoutil` (extract helper, add tests, update `Connect`).
2. Update `pkg/cassutil` (extract helper, add tests, update `Connect`).
3. Update each service `Config` struct and callsite, one service per commit (or grouped logically).
4. Run `make lint` and `make test` to confirm everything is green.
