package errcode

// Constant names carry the User domain prefix; wire string values are
// unprefixed, matching house style (cf. codes_room.go: RoomUserNotFound = "user_not_found").
const (
	UserAppNotFound          Reason = "app_not_found"
	UserAppDisabled          Reason = "app_disabled"
	UserInvalidDMTarget      Reason = "invalid_dm_target"
	UserSubscriptionNotFound Reason = "subscription_not_found"
)
