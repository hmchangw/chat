// Package catalog holds the Go types and loader for the YAML catalogs
// under <repo-root>/catalogs/. Each type corresponds 1:1 to a YAML file.
package catalog

// Catalog is the in-memory union of every YAML file under catalogs/.
type Catalog struct {
	Verbs    []Verb
	Readers  []Reader
	Services []Service
	Cast     Cast
	Matchers []Matcher
}

// Verb describes one interface-level primitive (the verb catalog entries).
type Verb struct {
	Name                    string         `yaml:"name"`
	Description             string         `yaml:"description"`
	InputShape              map[string]any `yaml:"input_shape"`
	TransportEffectTemplate []string       `yaml:"transport_effect_template"`
	Executor                string         `yaml:"executor"`
}

// Reader describes one bounded observation location.
type Reader struct {
	Location        string   `yaml:"location"`
	Owners          []string `yaml:"owners"`
	TimestampSource string   `yaml:"timestamp_source"`
	Shape           string   `yaml:"shape"`
	Executor        string   `yaml:"executor"`
}

// Service captures a service's ability profile (background activity it can
// legitimately do outside our cascade).
type Service struct {
	Name     string           `yaml:"service"`
	Triggers []map[string]any `yaml:"triggers"`
}

// Matcher is one named predicate function.
type Matcher struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Cast is the seeded fixture roster (users, rooms, messages, ...).
type Cast struct {
	Users         []CastUser         `yaml:"users"`
	Rooms         []CastRoom         `yaml:"rooms"`
	Messages      []CastMessage      `yaml:"messages"`
	Subscriptions []CastSubscription `yaml:"subscriptions"`
}

// CastUser is one user fixture with credentials and tags.
type CastUser struct {
	ID          string         `yaml:"id"`
	Tags        []string       `yaml:"tags"`
	Credentials map[string]any `yaml:"credentials"`
}

// CastRoom is one room fixture.
type CastRoom struct {
	ID   string   `yaml:"id"`
	Tags []string `yaml:"tags"`
}

// CastMessage is one message fixture.
type CastMessage struct {
	ID   string   `yaml:"id"`
	Tags []string `yaml:"tags"`
}

// CastSubscription is one subscription fixture.
type CastSubscription struct {
	ID   string   `yaml:"id"`
	Tags []string `yaml:"tags"`
}
