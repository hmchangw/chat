package errcode

import "encoding/json"

// Parse decodes a reply payload into an *Error iff it is an error envelope
// (non-empty "error" field). Returns (nil, false) for success payloads or garbage.
//
// Parse does NOT validate Code against the closed set — a malformed/foreign
// payload may yield a non-canonical Code. Callers that re-emit a remote
// envelope MUST check Code.Valid() before passing to New (which panics).
func Parse(data []byte) (*Error, bool) {
	var e Error
	//nolint:nilerr // a malformed payload is simply "not an error envelope"; the unmarshal error is intentionally not surfaced
	if err := json.Unmarshal(data, &e); err != nil || e.Message == "" {
		return nil, false
	}
	return &e, true
}
