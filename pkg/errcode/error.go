package errcode

// Error is the canonical client-facing error; marshals to {error, code, reason?, metadata?}.
// cause is unexported so encoding/json cannot leak it; reachable only via Unwrap. See doc.go.
type Error struct {
	Code     Code              `json:"code"`
	Reason   Reason            `json:"reason,omitempty"`
	Message  string            `json:"error"`
	Metadata map[string]string `json:"metadata,omitempty"`
	cause    error
}

// Error returns ONLY the user-safe message, never the cause.
func (e *Error) Error() string { return e.Message }

// Unwrap exposes the wrapped cause for errors.Is/As and server-side logging.
// JSON marshalling does not call Unwrap, so the cause never reaches clients.
func (e *Error) Unwrap() error { return e.cause }

// HTTPStatus returns the HTTP status for this error's category.
func (e *Error) HTTPStatus() int { return e.Code.HTTPStatus() }
