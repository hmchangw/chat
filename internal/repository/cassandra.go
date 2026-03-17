package repository

import (
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

const (
	keyspace = "chat"
	createKeyspaceQuery = `CREATE KEYSPACE IF NOT EXISTS chat
		WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`
	createTableQuery = `CREATE TABLE IF NOT EXISTS chat.messages (
		id UUID PRIMARY KEY,
		sender TEXT,
		content TEXT,
		created_at TIMESTAMP,
		updated_at TIMESTAMP
	)`
)

type CassandraConfig struct {
	Hosts    []string
	Port     int
	Username string
	Password string
	Timeout  time.Duration
}

func NewSession(cfg CassandraConfig) (*gocql.Session, error) {
	// First connect without keyspace to create it
	initCluster := gocql.NewCluster(cfg.Hosts...)
	initCluster.Port = cfg.Port
	initCluster.Timeout = cfg.Timeout
	if cfg.Username != "" {
		initCluster.Authenticator = gocql.PasswordAuthenticator{
			Username: cfg.Username,
			Password: cfg.Password,
		}
	}
	initCluster.Consistency = gocql.Quorum

	initSession, err := initCluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to cassandra: %w", err)
	}
	defer initSession.Close()

	if err := initSession.Query(createKeyspaceQuery).Exec(); err != nil {
		return nil, fmt.Errorf("failed to create keyspace: %w", err)
	}

	if err := initSession.Query(createTableQuery).Exec(); err != nil {
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	// Now connect with keyspace
	cluster := gocql.NewCluster(cfg.Hosts...)
	cluster.Port = cfg.Port
	cluster.Keyspace = keyspace
	cluster.Timeout = cfg.Timeout
	if cfg.Username != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: cfg.Username,
			Password: cfg.Password,
		}
	}
	cluster.Consistency = gocql.Quorum

	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create cassandra session: %w", err)
	}

	return session, nil
}
