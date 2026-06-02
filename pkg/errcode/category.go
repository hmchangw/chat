package errcode

// Code is the closed set of generic error classifications; drives HTTP status.
type Code string

const (
	CodeBadRequest      Code = "bad_request"
	CodeUnauthenticated Code = "unauthenticated"
	CodeForbidden       Code = "forbidden"
	CodeNotFound        Code = "not_found"
	CodeConflict        Code = "conflict"
	CodeTooManyRequests Code = "too_many_requests"
	CodeUnavailable     Code = "unavailable"
	CodeInternal        Code = "internal"
)

// Valid reports whether c is one of the canonical Code* constants.
func (c Code) Valid() bool {
	switch c {
	case CodeBadRequest, CodeUnauthenticated, CodeForbidden, CodeNotFound,
		CodeConflict, CodeTooManyRequests, CodeUnavailable, CodeInternal:
		return true
	default:
		return false
	}
}

// HTTPStatus maps a code to its HTTP status; unknown values map to 500 so a
// misclassification never leaks as 2xx.
func (c Code) HTTPStatus() int {
	switch c {
	case CodeBadRequest:
		return 400
	case CodeUnauthenticated:
		return 401
	case CodeForbidden:
		return 403
	case CodeNotFound:
		return 404
	case CodeConflict:
		return 409
	case CodeTooManyRequests:
		return 429
	case CodeUnavailable:
		return 503
	default:
		return 500
	}
}
