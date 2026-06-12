package errcode

// Reasons emitted by portal-service and the auth-service minting gate.
const (
	// PortalAccountNotProvisioned: authenticated but not in the per-site users collection (auth minting gate).
	PortalAccountNotProvisioned Reason = "account_not_provisioned"
	// PortalAccountNotReady: account absent from the portal's in-memory employee directory cache (portal lookup).
	PortalAccountNotReady Reason = "account_not_ready"
)
