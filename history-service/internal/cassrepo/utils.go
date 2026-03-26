package cassrepo

import "github.com/gocql/gocql"

// Page is a generic paginated result from Cassandra.
type Page[T any] struct {
	Items     []T    `json:"items"`
	PageState []byte `json:"pageState,omitempty"`
	HasMore   bool   `json:"hasMore"`
}

// Scanner scans values from a gocql iterator row into a T.
type Scanner[T any] func(iter *gocql.Iter) (T, error)

// Query wraps a CQL statement with builder-pattern configuration.
type Query struct {
	session   *gocql.Session
	stmt      string
	args      []interface{}
	pageSize  int
	pageState []byte
}

// NewQuery creates a new Query with the given statement and arguments.
func NewQuery(session *gocql.Session, stmt string, args ...interface{}) *Query {
	return &Query{
		session: session,
		stmt:    stmt,
		args:    args,
	}
}

// PageSize sets the number of results per page.
func (q *Query) PageSize(n int) *Query {
	q.pageSize = n
	return q
}

// WithPageState sets the pagination cursor for resuming iteration.
func (q *Query) WithPageState(state []byte) *Query {
	q.pageState = state
	return q
}

// ScanPage executes the query and scans results into a Page[T].
// Standalone function because Go methods cannot have type parameters.
func ScanPage[T any](q *Query, scan Scanner[T]) (*Page[T], error) {
	gocqlQuery := q.session.Query(q.stmt, q.args...)
	if q.pageSize > 0 {
		gocqlQuery = gocqlQuery.PageSize(q.pageSize)
	}
	if q.pageState != nil {
		gocqlQuery = gocqlQuery.PageState(q.pageState)
	}

	iter := gocqlQuery.Iter()
	var items []T
	for {
		item, err := scan(iter)
		if err != nil {
			break
		}
		items = append(items, item)
	}

	pageState := iter.PageState()
	if err := iter.Close(); err != nil {
		return nil, err
	}

	return &Page[T]{
		Items:     items,
		PageState: pageState,
		HasMore:   len(pageState) > 0,
	}, nil
}
