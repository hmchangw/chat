package cassrepo

import (
	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/msgbucket"
)

// Repository wraps a Cassandra session with the bucket sizer and read-walk
// configuration shared by all queries against bucketed message tables.
type Repository struct {
	session              *gocql.Session
	bucket               msgbucket.Sizer
	maxBuckets           int
	reactionsConcurrency int
}

// NewRepository wires a session, bucket sizer, and max-walk depth.
// maxBuckets caps how far a paginated read walks through empty buckets before
// returning a non-terminal cursor. reactionsConcurrency caps the per-request
// fan-out when loading reactions for a batch of messages.
func NewRepository(session *gocql.Session, bucket msgbucket.Sizer, maxBuckets, reactionsConcurrency int) *Repository {
	if reactionsConcurrency < 1 {
		reactionsConcurrency = 1
	}
	return &Repository{
		session:              session,
		bucket:               bucket,
		maxBuckets:           maxBuckets,
		reactionsConcurrency: reactionsConcurrency,
	}
}
