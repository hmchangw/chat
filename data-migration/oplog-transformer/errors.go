package main

import "errors"

// errPoison marks an event that can never succeed (unmappable doc). The consume loop Terms
// these instead of redelivering, so one bad event never wedges the stream.
var errPoison = errors.New("poison event")

// errSkipped marks an event the handler deliberately dropped. The consume loop Acks these but does
// NOT count them as processed — the skip is already metered via onSkipped, so counting it double-counts.
var errSkipped = errors.New("event skipped")
