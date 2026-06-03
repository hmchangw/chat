package errcode

// Reasons emitted by auth-service.
const (
	AuthTokenExpired   Reason = "sso_token_expired"
	AuthInvalidToken   Reason = "invalid_sso_token"
	AuthInvalidRequest Reason = "invalid_request"
	AuthInvalidNKey    Reason = "invalid_nkey"
	AuthMissingFields  Reason = "missing_fields"
)
