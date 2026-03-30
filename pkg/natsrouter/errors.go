package natsrouter

import "fmt"

// RouteError is an error that produces a user-facing response.
// When a handler returns a RouteError, the router sends it as the reply
// instead of the generic "internal error". Use this for expected error
// conditions that the client should see (not found, forbidden, validation, etc.).
//
// Any other error returned by a handler is treated as an internal error —
// it is logged and the client receives "internal error".
//
// Example:
//
//	func (s *Service) GetRoom(ctx context.Context, p Params, req GetRoomReq) (*Room, error) {
//	    room, err := s.store.Find(ctx, req.ID)
//	    if err != nil {
//	        return nil, fmt.Errorf("finding room: %w", err) // → "internal error" to client
//	    }
//	    if room == nil {
//	        return nil, natsrouter.Errorf("room %s not found", req.ID) // → sent to client as-is
//	    }
//	    return room, nil
//	}
type RouteError struct {
	Message string `json:"error"`
	Code    string `json:"code,omitempty"`
}

// Error implements the error interface.
func (e *RouteError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Message
}

// Err creates a RouteError with the given message.
// The client receives: {"error": "message"}
func Err(message string) *RouteError {
	return &RouteError{Message: message}
}

// Errf creates a RouteError with a formatted message.
// The client receives: {"error": "formatted message"}
func Errf(format string, args ...any) *RouteError {
	return &RouteError{Message: fmt.Sprintf(format, args...)}
}

// ErrWithCode creates a RouteError with a machine-readable code and message.
// The client receives: {"error": "message", "code": "code"}
//
// Common codes: "not_found", "forbidden", "bad_request", "conflict"
func ErrWithCode(code, message string) *RouteError {
	return &RouteError{Message: message, Code: code}
}
