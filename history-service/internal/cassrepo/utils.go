package cassrepo

import (
	"encoding/base64"
	"fmt"
	"reflect"

	"github.com/gocql/gocql"
)

// Cursor wraps Cassandra's PageState as a base64-encoded pagination token.
type Cursor struct {
	state []byte
}

// NewCursor decodes a base64-encoded cursor string; empty string returns the first-page cursor.
func NewCursor(encoded string) (*Cursor, error) {
	if encoded == "" {
		return &Cursor{}, nil
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxCursorBytes) {
		return nil, fmt.Errorf("decode cursor: encoded length %d exceeds maximum of %d",
			len(encoded), base64.StdEncoding.EncodedLen(maxCursorBytes))
	}
	state, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode cursor: %w", err)
	}
	return &Cursor{state: state}, nil
}

// Encode returns the base64 cursor string, or empty string when there are no more pages.
func (c *Cursor) Encode() string {
	if len(c.state) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(c.state)
}

func (c *Cursor) Raw() []byte { return c.state }

type Page[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"nextCursor,omitempty"`
	HasNext    bool   `json:"hasNext"`
}

type PageRequest struct {
	Cursor   *Cursor
	PageSize int
}

const (
	defaultCassPageSize = 50
	maxPageSize         = 100
)

// maxCursorBytes is the maximum number of raw bytes a decoded page-state cursor
// may occupy. Real Cassandra page state tokens are 10–100 bytes; 512 is
// generous while still blocking pathological allocations.
const maxCursorBytes = 512

// ParsePageRequest validates and normalises cursor+pageSize. Default 50, max 100.
func ParsePageRequest(cursorStr string, pageSize int) (PageRequest, error) {
	cursor, err := NewCursor(cursorStr)
	if err != nil {
		return PageRequest{}, fmt.Errorf("parse page request cursor: %w", err)
	}
	if pageSize <= 0 {
		pageSize = defaultCassPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return PageRequest{Cursor: cursor, PageSize: pageSize}, nil
}

type QueryBuilder struct {
	query    *gocql.Query
	cursor   *Cursor
	pageSize int
}

func NewQueryBuilder(q *gocql.Query) *QueryBuilder {
	return &QueryBuilder{query: q, pageSize: defaultCassPageSize}
}

func (b *QueryBuilder) WithCursor(cursor *Cursor) *QueryBuilder {
	b.cursor = cursor
	return b
}

func (b *QueryBuilder) WithPageSize(size int) *QueryBuilder {
	b.pageSize = size
	return b
}

// structScan scans the current row of iter into dest using cql struct tags for
// column-to-field mapping. It mirrors the gocql StructScan API that is not
// present in v1.7.0: it builds a map[string]interface{} of column-name →
// field-pointer from the dest struct's cql tags, then delegates to
// iter.MapScan so gocql handles all type unmarshalling (including UDTs).
// Returns true when a row was consumed, false when the iterator is exhausted
// or an error occurred.
//
// The column map is rebuilt on every call. Do NOT cache or reuse it across
// rows: iter.MapScan overwrites map entries with bare values after each call,
// so a reused map would no longer contain field-pointers on the next scan.
func structScan(iter *gocql.Iter, dest interface{}) bool {
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
		return false
	}
	rv = rv.Elem()
	rt := rv.Type()

	row := make(map[string]interface{}, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		tag := field.Tag.Get("cql")
		if tag == "" || tag == "-" {
			continue
		}
		row[tag] = rv.Field(i).Addr().Interface()
	}
	return iter.MapScan(row)
}

// Fetch executes the query; scan is called with the page iterator. Returns the encoded next-page cursor.
func (b *QueryBuilder) Fetch(scan func(iter *gocql.Iter)) (string, error) {
	if b.query == nil {
		return "", fmt.Errorf("execute paged query: nil query")
	}
	q := b.query.PageSize(b.pageSize)
	if b.cursor != nil {
		q = q.PageState(b.cursor.Raw())
	}

	iter := q.Iter()
	scan(iter)

	nextCursor := (&Cursor{state: iter.PageState()}).Encode()
	if err := iter.Close(); err != nil {
		return "", fmt.Errorf("close cassandra iterator: %w", err)
	}
	return nextCursor, nil
}
