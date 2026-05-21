package emoji

// builtinShortcodes is the static allowlist of Unicode-emoji shortcodes
// recognized server-side without a custom_emojis lookup. Keep entries
// lowercase and unwrapped (no surrounding colons). Add new entries as
// product needs grow; never remove an entry already used in stored data.
var builtinShortcodes = map[string]struct{}{
	// gestures
	"thumbsup":     {},
	"thumbsdown":   {},
	"+1":           {},
	"-1":           {},
	"clap":         {},
	"wave":         {},
	"raised_hands": {},
	"pray":         {},
	"ok_hand":      {},
	"point_up":     {},
	"muscle":       {},

	// faces
	"smile":      {},
	"laughing":   {},
	"joy":        {},
	"cry":        {},
	"sob":        {},
	"thinking":   {},
	"wink":       {},
	"heart_eyes": {},

	// hearts / love
	"heart":           {},
	"broken_heart":    {},
	"two_hearts":      {},
	"sparkling_heart": {},

	// reactions
	"tada":     {},
	"fire":     {},
	"eyes":     {},
	"100":      {},
	"check":    {},
	"x":        {},
	"warning":  {},
	"rocket":   {},
	"sparkles": {},
	"star":     {},
}

// IsBuiltinShortcode reports whether the given bare shortcode is in the
// built-in Unicode-emoji allowlist.
func IsBuiltinShortcode(shortcode string) bool {
	_, ok := builtinShortcodes[shortcode]
	return ok
}
