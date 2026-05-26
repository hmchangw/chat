package harness

// LastResponse captures the most recent request/reply outcome in a
// scenario — regardless of transport — so a `Then` step can assert
// on status, body, error class, and trace.
type LastResponse struct {
	Transport  string // "http" or "nats"
	StatusCode int    // HTTP status; 0 for NATS
	ErrorText  string // NATS reply body's "error" field; empty on success
	Body       []byte
	TraceID    string
	Err        error // transport-level error (NATS: ErrNoResponders, ErrTimeout, etc.)
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
	natsPool     *NATSConnPool
	// fixtures stores scenario-level fixture data keyed by a friendly name.
	// Steps use it to stash server-assigned IDs (e.g. room IDs from DM creation)
	// so later steps can look them up without setting LastResponse.
	fixtures map[string]string
}

// NewWorld creates a world for a single suite invocation.
func NewWorld(runID string) *World {
	return &World{
		runID:       runID,
		credentials: map[string]*Credentials{},
		natsPool:    NewNATSConnPool(),
		fixtures:    map[string]string{},
	}
}

// BeginScenario resets per-scenario state and installs a new IDPrefixer.
func (w *World) BeginScenario(name string) {
	if w.natsPool != nil {
		w.natsPool.CloseAll()
	}
	w.scenarioName = name
	w.prefix = NewIDPrefixer(w.runID, ScenarioIDFromName(name))
	w.lastResponse = nil
	w.credentials = map[string]*Credentials{} // reset per scenario
	w.natsPool = NewNATSConnPool()
	w.fixtures = map[string]string{}
}

// Prefix returns the IDPrefixer for the current scenario.
func (w *World) Prefix() *IDPrefixer { return w.prefix }

// RunID returns the run-level prefix.
func (w *World) RunID() string { return w.runID }

// ScenarioName returns the Gherkin name of the current scenario.
func (w *World) ScenarioName() string { return w.scenarioName }

// SetLastResponse records the most recent request/reply outcome.
func (w *World) SetLastResponse(r *LastResponse) { w.lastResponse = r }

// LastResponse returns the most recent request/reply outcome, or nil.
func (w *World) LastResponse() *LastResponse { return w.lastResponse }

// NATSPool returns the per-scenario NATS connection pool.
func (w *World) NATSPool() *NATSConnPool { return w.natsPool }

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

// SetFixture stores an arbitrary string value under a friendly name.
// Used by Given (setup) steps to stash server-assigned IDs so later steps
// can retrieve them without using LastResponse.
func (w *World) SetFixture(name, value string) { w.fixtures[name] = value }

// Fixture retrieves a value previously stored with SetFixture.
// Returns ("", false) if no value is stored under that name.
func (w *World) Fixture(name string) (string, bool) {
	v, ok := w.fixtures[name]
	return v, ok
}
