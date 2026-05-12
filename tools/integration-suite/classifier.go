package integrationsuite

import (
	"bytes"
	"strings"
)

// Class is one of the 8 error categories used to break down failures.
// Source: spec §"Traceability and error classification".
type Class string

const (
	ClassNone          Class = "None"
	ClassRouteNotFound Class = "RouteNotFound"
	ClassValidation    Class = "Validation"
	ClassAuth          Class = "Auth"
	ClassHandlerError  Class = "HandlerError"
	ClassTimeout       Class = "Timeout"
	ClassUnreachable   Class = "Unreachable"
	ClassPersistence   Class = "Persistence"
	ClassDownstream    Class = "Downstream"
	ClassUnclassified  Class = "Unclassified"
)

// ClassifyHTTP returns the Class for an HTTP response.
// `body` may be nil; classification falls back to status-code-only rules.
func ClassifyHTTP(statusCode int, body []byte) Class {
	if statusCode >= 200 && statusCode < 300 {
		return ClassNone
	}

	if statusCode == 401 || statusCode == 403 {
		return ClassAuth
	}
	if statusCode == 400 {
		return ClassValidation
	}
	if statusCode == 404 || statusCode == 409 || statusCode == 422 {
		return ClassHandlerError
	}

	if statusCode >= 500 && statusCode < 600 {
		// 5xx: inspect body for hints to narrow the class.
		if statusCode == 504 || bodyContainsCode(body, "REQUEST_TIMEOUT", "TIMEOUT") {
			return ClassTimeout
		}
		if bodyContainsCode(body, "DB_", "MONGO_", "CASSANDRA_") {
			return ClassPersistence
		}
		return ClassDownstream
	}

	return ClassUnclassified
}

// bodyContainsCode returns true if any of the substrings appears
// inside a JSON "code" field in the body. Case-insensitive contains —
// good enough for v1, since "code" values are uppercase by convention.
func bodyContainsCode(body []byte, needles ...string) bool {
	if len(body) == 0 {
		return false
	}
	upper := bytes.ToUpper(body)
	for _, n := range needles {
		if bytes.Contains(upper, []byte(strings.ToUpper(n))) {
			return true
		}
	}
	return false
}
