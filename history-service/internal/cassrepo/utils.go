package cassrepo

import (
	"encoding/base64"

	"github.com/gocql/gocql"
)

// Cursor is an opaque pagination token wrapping Cassandra's PageState.
// It is base64-encoded for safe transport in JSON and URLs.
type Cursor struct {
	state []byte
}

// NewCursor decodes a base64-encoded cursor string.
// An empty string returns a zero cursor (first page).
func NewCursor(encoded string) (*Cursor, error) {
	if encoded == "" {
		return &Cursor{}, nil
	}
	state, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	return &Cursor{state: state}, nil
}

// Encode returns the base64 representation of the cursor.
// Returns empty string for a zero cursor (no more pages).
func (c *Cursor) Encode() string {
	if len(c.state) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(c.state)
}

// Raw returns the underlying Cassandra PageState bytes.
func (c *Cursor) Raw() []byte {
	return c.state
}

// Page is a generic paginated result from Cassandra.
type Page[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"nextCursor,omitempty"`
	HasNext    bool   `json:"hasNext"`
}

// PageRequest represents a pagination request from the client.
type PageRequest struct {
	Cursor   *Cursor
	PageSize int
}

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

// ParsePageRequest creates a PageRequest from a cursor string and page size.
// Returns a valid PageRequest with defaults applied for invalid/missing values.
// Default page size is 50, max is 100.
func ParsePageRequest(cursorStr string, pageSize int) (PageRequest, error) {
	cursor, err := NewCursor(cursorStr)
	if err != nil {
		return PageRequest{}, err
	}
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return PageRequest{Cursor: cursor, PageSize: pageSize}, nil
}

// QueryBuilder wraps a *gocql.Query with pagination support.
// Use NewQueryBuilder to create, then chain WithCursor and WithPageSize,
// and call Fetch to execute.
type QueryBuilder struct {
	query    *gocql.Query
	cursor   *Cursor
	pageSize int
}

// NewQueryBuilder wraps an existing gocql.Query with pagination.
func NewQueryBuilder(q *gocql.Query) *QueryBuilder {
	return &QueryBuilder{query: q, pageSize: 10}
}

// WithCursor sets the pagination cursor for resuming from a previous page.
func (b *QueryBuilder) WithCursor(cursor *Cursor) *QueryBuilder {
	b.cursor = cursor
	return b
}

// WithPageSize sets the number of results per page.
func (b *QueryBuilder) WithPageSize(size int) *QueryBuilder {
	b.pageSize = size
	return b
}

// Fetch executes the query and passes the iterator to the callback for scanning.
// The caller controls the scan loop. Returns the encoded next cursor and any error.
func (b *QueryBuilder) Fetch(scan func(iter *gocql.Iter)) (string, error) {
	q := b.query.PageSize(b.pageSize)
	if b.cursor != nil {
		q = q.PageState(b.cursor.Raw())
	}

	iter := q.Iter()
	scan(iter)

	nextCursor := (&Cursor{state: iter.PageState()}).Encode()
	return nextCursor, iter.Close()
}
