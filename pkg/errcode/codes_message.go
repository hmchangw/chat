package errcode

// Reasons emitted by message-gatekeeper and history-service.
const (
	MessageLargeRoomPostRestricted Reason = "large_room_post_restricted"
	MessageNotSubscribed           Reason = "not_subscribed"
	// MessageOutsideAccessWindow distinguishes "caller IS subscribed but the
	// message predates HSS" from MessageNotSubscribed — the frontend renders
	// different UX ("history hidden before you joined").
	MessageOutsideAccessWindow Reason = "outside_access_window"
)
