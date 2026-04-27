package cassutil

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/gocql/gocql"
)

func Connect(hosts []string, keyspace, username, password string) (*gocql.Session, error) {
	cluster := buildCluster(hosts, keyspace, username, password)

	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("cassandra connect: %w", err)
	}
	slog.Info("connected to Cassandra", "keyspace", keyspace)
	return session, nil
}

func Close(session *gocql.Session) {
	session.Close()
}

func buildCluster(hosts []string, keyspace, username, password string) *gocql.ClusterConfig {
	cluster := gocql.NewCluster(hosts...)
	cluster.Keyspace = keyspace
	cluster.Consistency = gocql.LocalQuorum
	cluster.Timeout = 10 * time.Second
	if username != "" && password != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: username,
			Password: password,
		}
	}
	return cluster
}
