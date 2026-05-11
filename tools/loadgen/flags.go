package main

import (
	"errors"
	"fmt"
	"time"
)

// parseInjectMode maps the --inject string to its enum. Pure function;
// the only side-effect is the error case. Extracted from runRun for
// unit-testability.
func parseInjectMode(s string) (InjectMode, error) {
	switch s {
	case "frontdoor":
		return InjectFrontdoor, nil
	case "canonical":
		return InjectCanonical, nil
	default:
		return "", fmt.Errorf("unknown inject mode: %s (want frontdoor|canonical)", s)
	}
}

// parseScenarioFlag validates the --scenario string against the
// allow-list. Returns nil when valid.
func parseScenarioFlag(s string) error {
	switch s {
	case "messaging-pipeline", "history-read", "search-read", "room-rpc":
		return nil
	default:
		return fmt.Errorf("unknown scenario: %s", s)
	}
}

// errMissingRampFields is returned when only some --ramp-* fields are
// set. Either all three of from/to/duration are positive, or all three
// are zero (no ramp). Mixed input is a config error.
var errMissingRampFields = errors.New("--ramp-from, --ramp-to, --ramp-duration must all be > 0 when ramping")

// parseRampShape maps the --ramp-shape string to its enum.
func parseRampShape(s string) (RampShape, error) {
	switch s {
	case "linear":
		return RampLinear, nil
	case "exponential":
		return RampExponential, nil
	default:
		return 0, fmt.Errorf("unknown ramp shape: %s (want linear|exponential)", s)
	}
}

// buildRamp constructs a *Ramp from the four --ramp-* flag values.
// Returns (nil, nil) when no ramp is requested (all three numeric
// fields are 0). Returns an error if the user set only some fields
// or supplied an invalid shape.
func buildRamp(from, to int, dur time.Duration, shape string) (*Ramp, error) {
	if from <= 0 && to <= 0 && dur <= 0 {
		return nil, nil // no ramp configured
	}
	if from <= 0 || to <= 0 || dur <= 0 {
		return nil, errMissingRampFields
	}
	rs, err := parseRampShape(shape)
	if err != nil {
		return nil, err
	}
	return &Ramp{From: from, To: to, Duration: dur, Shape: rs}, nil
}
