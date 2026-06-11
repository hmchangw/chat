package mongoutil

type OffsetPageRequest struct {
	Offset int64
	Limit  int64
}

type OffsetPage[T any] struct {
	Data  []T
	Total int64
}

// EmptyPage returns a zero-result page with non-nil Data so JSON marshals to [] not null.
func EmptyPage[T any]() OffsetPage[T] {
	return OffsetPage[T]{Data: []T{}}
}

// NewOffsetPageRequestWithBounds validates offset+limit against caller-supplied
// bounds: limit <= 0 -> defaultLimit, limit > maxLimit -> maxLimit, negative offset -> 0.
func NewOffsetPageRequestWithBounds(offset, limit, defaultLimit, maxLimit int) OffsetPageRequest {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return OffsetPageRequest{Offset: int64(offset), Limit: int64(limit)}
}

// NewOffsetPageRequest validates offset+limit. Default limit 20, max 100, negative offset clamped to 0.
func NewOffsetPageRequest(offset, limit int) OffsetPageRequest {
	return NewOffsetPageRequestWithBounds(offset, limit, 20, 100)
}
