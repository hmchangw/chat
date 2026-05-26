// Package mishap holds the perturbation catalog: each mishap is an
// action the platform applies during a test case (kill a pod, run
// concurrently, etc.). Mishaps do NOT declare expected behavior; the
// scenario's reads remain authoritative under any subset.
package mishap

// Mishap is one named perturbation.
type Mishap struct {
	Name string
	Axis string // "environment" or "concurrency"
}

// Executor performs the perturbation during a test case.
// The runner calls Apply before the verb fires (or in parallel with it,
// per mishap semantics) and Cleanup after the case.
type Executor interface {
	Apply() error
	Cleanup() error
}
