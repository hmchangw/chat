// Package scenario holds the Go types for parsed scenario YAML files.
package scenario

// Scenario is one authored test template.
type Scenario struct {
	Name     string  `yaml:"scenario"`
	Source   string  `yaml:"source"`
	Input    Input   `yaml:"input"`
	Sequence []Step  `yaml:"sequence"`
	Mishaps  Mishaps `yaml:"mishaps"`
	Status   string  `yaml:"status,omitempty"`
	// Kind distinguishes the assertion class: "positive" scenarios
	// assert the system DOES the thing (happy paths); "negative"
	// scenarios assert the system correctly REJECTS the thing
	// (validation, permission, precondition failures). Drives the
	// run report's confusion-matrix axis. Defaults to "positive"
	// when omitted (the common case).
	Kind string `yaml:"kind,omitempty"`
}

// Input is the verb invocation: which verb + how it's parameterized.
type Input struct {
	Verb         string                 `yaml:"verb"`
	Subject      string                 `yaml:"subject"`
	Payload      map[string]any         `yaml:"payload"`
	Credential   string                 `yaml:"credential"`
	Placeholders map[string]Placeholder `yaml:"placeholders"`
}

// Placeholder is a typed fixture slot with a predicate.
type Placeholder struct {
	Type      string         `yaml:"type"`
	Predicate map[string]any `yaml:"predicate"`
}

// Step is one ordered item in the observation sequence.
type Step struct {
	Service string `yaml:"service"`
	Reads   []Read `yaml:"reads"`
}

// Read is one expected observation at a location.
type Read struct {
	Location string `yaml:"location"`
	Matcher  string `yaml:"matcher"`
	Expected any    `yaml:"expected"`
	Within   string `yaml:"within,omitempty"`
	Optional bool   `yaml:"optional,omitempty"`
}

// Mishaps is the per-scenario control rod.
type Mishaps struct {
	Ignore []MishapIgnore `yaml:"ignore"`
}

// MishapIgnore names a mishap to skip with a one-line reason.
type MishapIgnore struct {
	Mishap string `yaml:"mishap"`
	Reason string `yaml:"reason"`
}
