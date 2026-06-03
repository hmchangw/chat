package errcode

import "errors"

// ErrPermanent is the sentinel callers match via errors.Is to detect a
// non-retryable job failure. Wrap with Permanent to mark one.
var ErrPermanent = errors.New("permanent")

// PermanentError marks an *Error as non-retryable: JetStream consumers Ack
// (drop) rather than Nak. Permanence is INDEPENDENT of category — an Internal
// can be permanent; a retryable infra error stays unwrapped.
type PermanentError struct{ ec *Error }

// Permanent wraps an *Error as a non-retryable failure. Panics on nil — a
// caller with no classified error to wrap is a programmer bug.
func Permanent(ec *Error) *PermanentError {
	if ec == nil {
		panic("errcode.Permanent: nil *Error")
	}
	return &PermanentError{ec: ec}
}

// Error returns the wrapped *Error's message.
func (p *PermanentError) Error() string { return p.ec.Error() }

// Unwrap exposes the wrapped *Error (and, transitively, its WithCause cause).
func (p *PermanentError) Unwrap() error { return p.ec }

// Is matches the ErrPermanent sentinel so callers branch on permanence without
// importing the concrete type.
func (p *PermanentError) Is(target error) bool { return target == ErrPermanent }

// IsPermanent reports whether err's chain carries a *PermanentError, returning
// the wrapped *Error. Returns (nil, false) for any non-permanent error.
func IsPermanent(err error) (*Error, bool) {
	var p *PermanentError
	if errors.As(err, &p) {
		return p.ec, true
	}
	return nil, false
}
