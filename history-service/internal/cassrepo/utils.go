package cassrepo

import (
	"encoding/base64"
	"fmt"
	"log/slog"
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

// buildScanValues maps colNames (in order) to addressable field pointers from
// dest using cql struct tags. It returns the pointer slice ready for
// iter.Scan, or the first unmapped column name if any column has no
// matching tag. dest must be a non-nil pointer to a struct.
//
// Separated from structScan so the column-matching logic is unit-testable
// without a live gocql iterator.
func buildScanValues(dest any, colNames []string) (values []any, missingCol string, ok bool) {
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
		return nil, "", false
	}
	rv = rv.Elem()
	rt := rv.Type()

	fieldByTag := make(map[string]reflect.Value, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		tag := field.Tag.Get("cql")
		if tag == "" || tag == "-" {
			continue
		}
		fieldByTag[tag] = rv.Field(i)
	}

	vals := make([]any, len(colNames))
	for i, name := range colNames {
		fv, found := fieldByTag[name]
		if !found {
			return nil, name, false
		}
		vals[i] = fv.Addr().Interface()
	}
	return vals, "", true
}

// structScan scans the current row of iter into dest using cql struct tags for
// column-to-field mapping. It mirrors the gocql StructScan API that is not
// present in v1.7.0: it inspects dest's cql tags to build a column-name →
// field-pointer index, then issues a positional iter.Scan in the column order
// declared by the result metadata.
//
// Why not iter.MapScan? MapScan internally calls iter.RowData(), which invokes
// column.TypeInfo.NewWithError() on every returned column to allocate a default
// destination — even for columns the caller already provided. For a
// MAP<frozen<UDT>, frozen<UDT>> column (e.g. the v3 reactions column), that
// path resolves the UDT goType to map[string]interface{} and then calls
// reflect.MapOf(map, map), which panics with "invalid key type
// map[string]interface{}" because Go maps are not comparable and cannot be
// used as map keys. Positional iter.Scan never builds default destinations,
// so it sidesteps the panic — gocql's reflective unmarshalMap is happy to
// write into our concrete *Reactions destination directly.
//
// Returns (true, nil) when a row was scanned successfully. Returns (false, nil)
// when the iterator is exhausted. Returns (false, non-nil error) when a result
// column has no matching cql tag on dest — the caller must treat this as a hard
// failure (every selected column must be addressable on the destination struct).
// An unmapped column also emits a slog.Warn for diagnostic visibility.
func structScan(iter *gocql.Iter, dest any) (bool, error) {
	// Validate dest shape before touching the iterator (iter may be nil in tests).
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
		return false, nil
	}

	cols := iter.Columns()
	colNames := make([]string, len(cols))
	for i, c := range cols {
		colNames[i] = c.Name
	}

	values, missingCol, ok := buildScanValues(dest, colNames)
	if !ok {
		err := fmt.Errorf("structScan: unmapped column %q for type %T", missingCol, dest)
		slog.Warn("structScan: unmapped column", "column", missingCol, "type", fmt.Sprintf("%T", dest))
		return false, err
	}
	return iter.Scan(values...), nil
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
