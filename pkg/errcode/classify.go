package errcode

import (
	"context"
	"errors"
	"log/slog"
)

// Classify converts any error into a client-safe *Error and logs it exactly once.
// Server faults log at ERROR, expected client errors at INFO. See doc.go.
func Classify(ctx context.Context, err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	var underlying string
	hasErrcode := errors.As(err, &e)
	if hasErrcode {
		if e.cause != nil {
			underlying = e.cause.Error()
		}
	} else {
		e = &Error{Code: CodeInternal, Message: "internal error", cause: err}
	}
	// Only compute the cause string when it's actually distinct from the
	// message — for a bare errcode.BadRequest("x") it would just duplicate the
	// "code"/"reason" attrs and waste the err.Error() allocation per 4xx.
	cause := e.Message
	if !hasErrcode || err != e {
		cause = err.Error()
	}
	attrs := []any{
		"code", string(e.Code),
		"reason", string(e.Reason),
		"cause", cause,
	}
	if underlying != "" {
		attrs = append(attrs, "underlying", underlying)
	}
	loggerFrom(ctx).Log(ctx, e.logLevel(), "request failed", attrs...)
	return e
}

func (e *Error) logLevel() slog.Level {
	switch e.Code {
	case CodeInternal, CodeUnavailable:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
