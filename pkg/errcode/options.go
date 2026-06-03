package errcode

import "errors"

// Option configures an *Error during construction.
type Option func(*Error)

// New builds an *Error; prefer the named constructors. Panics on a
// non-canonical Code or empty message — both are programmer errors.
func New(code Code, message string, opts ...Option) *Error {
	if !code.Valid() {
		panic("errcode: New called with non-canonical Code " + string(code) +
			" — use one of the named constructors (NotFound, Forbidden, ...) " +
			"or pass a Code* constant")
	}
	if message == "" {
		panic("errcode: empty message — every constructor requires user-safe text")
	}
	e := &Error{Code: code, Message: message}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Named constructors are the entire constructor API: one per category. No *f
// variants — they would swallow trailing Option args; pass fmt.Sprintf as msg.
func BadRequest(msg string, opts ...Option) *Error { return New(CodeBadRequest, msg, opts...) }
func Unauthenticated(msg string, opts ...Option) *Error {
	return New(CodeUnauthenticated, msg, opts...)
}
func Forbidden(msg string, opts ...Option) *Error { return New(CodeForbidden, msg, opts...) }
func NotFound(msg string, opts ...Option) *Error  { return New(CodeNotFound, msg, opts...) }
func Conflict(msg string, opts ...Option) *Error  { return New(CodeConflict, msg, opts...) }
func TooManyRequests(msg string, opts ...Option) *Error {
	return New(CodeTooManyRequests, msg, opts...)
}
func Unavailable(msg string, opts ...Option) *Error { return New(CodeUnavailable, msg, opts...) }
func Internal(msg string, opts ...Option) *Error    { return New(CodeInternal, msg, opts...) }

// WithReason attaches the specific machine code the frontend switches on.
func WithReason(r Reason) Option { return func(e *Error) { e.Reason = r } }

// WithMetadata attaches CLIENT-VISIBLE key/value metadata to the wire envelope
// (use WithLogValues for server-internal detail). Panics on odd len(kv).
func WithMetadata(kv ...string) Option {
	return func(e *Error) {
		if len(kv)%2 != 0 {
			panic("errcode: WithMetadata requires an even number of args (key/value pairs)")
		}
		if e.Metadata == nil {
			e.Metadata = make(map[string]string, len(kv)/2)
		}
		for i := 0; i < len(kv); i += 2 {
			e.Metadata[kv[i]] = kv[i+1]
		}
	}
}

// WithCause attaches a raw infra/third-party error for server-side logging.
// PANICS if err already carries an *errcode.Error (one-per-chain invariant). See doc.go.
func WithCause(err error) Option {
	return func(e *Error) {
		var nested *Error
		if errors.As(err, &nested) {
			panic("errcode: WithCause must not wrap another *errcode.Error; " +
				`propagate it with "return err" or fmt.Errorf("...: %w", err) instead`)
		}
		e.cause = err
	}
}
