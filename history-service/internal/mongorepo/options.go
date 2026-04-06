package mongorepo

import "go.mongodb.org/mongo-driver/v2/mongo/options"

// queryOptions holds internal query configuration set via functional options.
type queryOptions struct {
	projection any
	sort       any
	limit      *int64
	skip       *int64
}

// findOneOpts produces mongo FindOne options. Only projection is applied —
// sort, limit, and skip are not relevant for single-document lookups.
func (qo *queryOptions) findOneOpts() *options.FindOneOptionsBuilder {
	opts := options.FindOne()
	if qo.projection != nil {
		opts.SetProjection(qo.projection)
	}
	return opts
}

// findOpts produces mongo Find options with all applicable settings.
func (qo *queryOptions) findOpts() *options.FindOptionsBuilder {
	opts := options.Find()
	if qo.projection != nil {
		opts.SetProjection(qo.projection)
	}
	if qo.sort != nil {
		opts.SetSort(qo.sort)
	}
	if qo.limit != nil {
		opts.SetLimit(*qo.limit)
	}
	if qo.skip != nil {
		opts.SetSkip(*qo.skip)
	}
	return opts
}

// QueryOption is a functional option for configuring queries.
type QueryOption func(*queryOptions)

// WithProjection sets which fields to include or exclude from results.
// Use bson.M{"field": 1} to include, bson.M{"field": 0} to exclude.
func WithProjection(projection any) QueryOption {
	return func(o *queryOptions) {
		o.projection = projection
	}
}

// WithSort sets the sort order for results. Only applies to FindMany.
// Use bson.M{"field": 1} for ascending, bson.M{"field": -1} for descending.
func WithSort(sort any) QueryOption {
	return func(o *queryOptions) {
		o.sort = sort
	}
}

// WithLimit sets the maximum number of results to return. Only applies to FindMany.
func WithLimit(limit int64) QueryOption {
	return func(o *queryOptions) {
		o.limit = &limit
	}
}

// WithSkip sets the number of results to skip. Only applies to FindMany.
func WithSkip(skip int64) QueryOption {
	return func(o *queryOptions) {
		o.skip = &skip
	}
}

func apply(opts []QueryOption) *queryOptions {
	qo := &queryOptions{}
	for _, opt := range opts {
		opt(qo)
	}
	return qo
}
