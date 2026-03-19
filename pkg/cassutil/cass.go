package cassutil

import (
	"fmt"
	"log"
	"time"

	"github.com/gocql/gocql"
)

func Connect(hosts []string, keyspace string) (*gocql.Session, error) {
	cluster := gocql.NewCluster(hosts...)
	cluster.Keyspace = keyspace
	cluster.Consistency = gocql.LocalQuorum
	cluster.Timeout = 10 * time.Second

	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("cassandra connect: %w", err)
	}
	log.Printf("connected to Cassandra keyspace %q", keyspace)
	return session, nil
}

func Close(session *gocql.Session) {
	session.Close()
}
