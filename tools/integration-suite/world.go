package integrationsuite

// LastResponse captures the most recent HTTP response in a scenario,
// so a `Then` step can assert on status, body, and trace.
type LastResponse struct {
	StatusCode int
	Body       []byte
	TraceID    string
}

// World is the per-scenario shared state passed to step definitions.
// One World is created per `go test` invocation; BeginScenario resets
// scenario-scoped fields.
type World struct {
	runID string

	scenarioName string
	prefix       *IDPrefixer
	lastResponse *LastResponse
	credentials  map[string]*Credentials // keyed by account name (unprefixed)
}

// NewWorld creates a world for a single suite invocation.
func NewWorld(runID string) *World {
	return &World{runID: runID, credentials: map[string]*Credentials{}}
}

// BeginScenario resets per-scenario state and installs a new IDPrefixer.
func (w *World) BeginScenario(name string) {
	w.scenarioName = name
	w.prefix = NewIDPrefixer(w.runID, ScenarioIDFromName(name))
	w.lastResponse = nil
	w.credentials = map[string]*Credentials{} // reset per scenario
}

// Prefix returns the IDPrefixer for the current scenario.
func (w *World) Prefix() *IDPrefixer { return w.prefix }

// RunID returns the run-level prefix.
func (w *World) RunID() string { return w.runID }

// ScenarioName returns the Gherkin name of the current scenario.
func (w *World) ScenarioName() string { return w.scenarioName }

// SetLastResponse records the most recent HTTP response.
func (w *World) SetLastResponse(r *LastResponse) { w.lastResponse = r }

// LastResponse returns the most recent HTTP response, or nil.
func (w *World) LastResponse() *LastResponse { return w.lastResponse }

// SetCredentials stores per-user credentials for the current scenario.
// Account is the unprefixed name the scenario uses (e.g., "alice").
func (w *World) SetCredentials(account string, c *Credentials) {
	w.credentials[account] = c
}

// Credentials returns the stored credentials for an account, or nil
// if the scenario hasn't authenticated that user.
func (w *World) Credentials(account string) *Credentials {
	return w.credentials[account]
}
