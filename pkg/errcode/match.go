package errcode

import "errors"

// ReasonOf returns the Reason of the first *Error in err's chain, or "".
func ReasonOf(err error) Reason {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ""
}

// HasReason reports whether err's chain carries an *Error with reason r.
func HasReason(err error, r Reason) bool { return ReasonOf(err) == r }
