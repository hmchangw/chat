package runtime

import (
	"fmt"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/scenario"
)

// Verdict is the outcome of one test case.
type Verdict struct {
	Outcome            string
	MissingPositives   []ReadFailure
	UnexpectedCascades []readers.Event
	Anomalies          []readers.Event
	Reason             string
	Steps              []StepResult // per-step outcome; populated by Classify
}

// StepResult is one sequence-step's outcome — pass if all of the
// step's expected reads matched, fail otherwise. MissingPositives
// here are filtered to those attributed to this step's service
// (scenario-level cascades/anomalies stay on the parent Verdict).
//
// Duration is the delta-from-previous-step attribution: the time
// between the last matched event of the previous step (or T_open for
// the first step) and this step's last matched event. It surfaces
// which service is the bottleneck in a long pipeline. Zero if the
// step had no matched events (e.g., failed steps).
type StepResult struct {
	Service          string
	Outcome          string
	Duration         time.Duration
	MissingPositives []ReadFailure
}

// ReadFailure describes one expected read that didn't match.
type ReadFailure struct {
	Service  string
	Location string
	Reason   string
	Expected any // The read's `expected:` value, post-substitution. Surfaced in the report so authors can see what didn't appear.
}

// AbilityProfileLookup returns whether a service has an ability profile
// pattern that matches an observed event without our trace.
type AbilityProfileLookup func(serviceName string, ev readers.Event) bool

// Classify applies the three-way model + missing-positive check.
// subCtx, if non-nil, drives ${…} substitution on each read's
// Expected before passing to the matcher. Pass &Context{} when no
// substitution is needed. tOpen is the scenario fire time used as the
// anchor for the first step's duration; pass a zero time to disable
// duration attribution.
func Classify(
	s *scenario.Scenario,
	events []readers.Event,
	traceID string,
	matcherReg *matchers.Registry,
	profileLookup AbilityProfileLookup,
	subCtx *Context,
	tOpen time.Time,
) Verdict {
	v := Verdict{Outcome: "pass"}

	type expectedFlat struct {
		Service  string
		Location string
		StepIdx  int
		Read     scenario.Read
	}
	var flat []expectedFlat
	for stepIdx, step := range s.Sequence {
		for _, r := range step.Reads {
			flat = append(flat, expectedFlat{Service: step.Service, Location: r.Location, StepIdx: stepIdx, Read: r})
		}
	}

	// Pre-substitution pass: resolve ${…} tokens in each read's Expected
	// against subCtx. Substitution errors fail the whole scenario fast
	// with a descriptive reason — this is an authoring bug.
	if subCtx != nil {
		for i := range flat {
			substituted, err := Substitute(flat[i].Read.Expected, *subCtx)
			if err != nil {
				return Verdict{
					Outcome: "fail",
					Reason:  fmt.Sprintf("substitute expected for %s/%s: %v", flat[i].Service, flat[i].Location, err),
				}
			}
			flat[i].Read.Expected = substituted
		}
	}

	satisfied := map[int]bool{}
	// stepLastTs[stepIdx] is the latest matched event timestamp seen for
	// that step. We use it to attribute per-step duration as a delta from
	// the previous step's last match (or T_open for step 0).
	stepLastTs := make(map[int]time.Time, len(s.Sequence))

	for _, ev := range events {
		if hasTrace(ev.Traceparent, traceID) {
			matched := false
			for i, e := range flat {
				if satisfied[i] {
					continue
				}
				if e.Location != ev.Location {
					continue
				}
				m, err := matcherReg.Get(e.Read.Matcher)
				if err != nil {
					continue
				}
				if m.Match(ev.Payload, e.Read.Expected).Matched {
					satisfied[i] = true
					matched = true
					if cur, ok := stepLastTs[e.StepIdx]; !ok || ev.Timestamp.After(cur) {
						stepLastTs[e.StepIdx] = ev.Timestamp
					}
					break
				}
			}
			if !matched {
				v.UnexpectedCascades = append(v.UnexpectedCascades, ev)
			}
		} else {
			if profileLookup != nil && profileLookup(ev.OwnerSvc, ev) {
				continue
			}
			v.Anomalies = append(v.Anomalies, ev)
		}
	}

	for i, e := range flat {
		if satisfied[i] {
			continue
		}
		if e.Read.Optional {
			continue
		}
		v.MissingPositives = append(v.MissingPositives, ReadFailure{
			Service:  e.Service,
			Location: e.Location,
			Reason:   "expected read did not match within timeframe",
			Expected: e.Read.Expected,
		})
	}

	// Per-step outcome: a step passes iff all of its expected reads
	// were satisfied. Missing-positives attributed to a step's service
	// land on that step.
	mpByService := map[string][]ReadFailure{}
	for _, mp := range v.MissingPositives {
		mpByService[mp.Service] = append(mpByService[mp.Service], mp)
	}
	// Duration attribution: walk the sequence in order and compute each
	// step's delta from the previous step's last-matched event (or
	// tOpen for step 0). Steps with no matched events get zero — and
	// don't advance the anchor, so the next step's window still starts
	// from the last known timestamp.
	prevTs := tOpen
	for stepIdx, step := range s.Sequence {
		sr := StepResult{Service: step.Service, Outcome: "pass"}
		if mps, ok := mpByService[step.Service]; ok && len(mps) > 0 {
			sr.Outcome = "fail"
			sr.MissingPositives = mps
		}
		if ts, ok := stepLastTs[stepIdx]; ok && !tOpen.IsZero() && !prevTs.IsZero() && ts.After(prevTs) {
			sr.Duration = ts.Sub(prevTs)
			prevTs = ts
		}
		v.Steps = append(v.Steps, sr)
	}

	if len(v.MissingPositives) > 0 || len(v.UnexpectedCascades) > 0 || len(v.Anomalies) > 0 {
		v.Outcome = "fail"
	}
	return v
}
