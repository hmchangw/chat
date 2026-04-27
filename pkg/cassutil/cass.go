package cassutil

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gocql/gocql"
)

func Connect(hosts, keyspace, username, password string) (*gocql.Session, error) {
	cluster := buildCluster(parseHosts(hosts), keyspace, username, password)

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

func parseHosts(s string) []string {
	var hosts []string
	for _, h := range strings.Split(s, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		hosts = append(hosts, h)
	}
	return hosts
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
