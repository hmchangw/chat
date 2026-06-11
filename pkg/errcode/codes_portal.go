package errcode

// Reasons emitted by portal-service.
const (
	// PortalAccountNotReady: no ready directory record (no record, empty
	// siteId, or unconfigured siteId). Server log carries the deny_case.
	PortalAccountNotReady Reason = "account_not_ready"
)
