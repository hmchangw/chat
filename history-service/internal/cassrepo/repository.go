package cassrepo

import "github.com/gocql/gocql"

// Repository implements service.MessageRepository using Cassandra.
type Repository struct {
	session *gocql.Session
}

// NewRepository creates a new Cassandra repository.
func NewRepository(session *gocql.Session) *Repository {
	return &Repository{session: session}
}
