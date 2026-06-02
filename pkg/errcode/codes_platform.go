package errcode

// Reasons emitted by cross-cutting platform middleware (pkg/natsutil, pkg/natsrouter)
// rather than a single domain service.
const (
	// RequestIDRequired marks a rejected request that arrived without a valid
	// X-Request-ID on a dedup-critical path (see natsutil.RequireRequestID).
	// Clients should special-case it by retrying with a freshly minted
	// hyphenated UUID rather than surfacing a generic "bad request".
	RequestIDRequired Reason = "request_id_required"
)
