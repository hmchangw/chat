package matchers

import "fmt"

// Registry maps matcher names to Matcher implementations.
type Registry struct {
	m map[string]Matcher
}

// NewRegistry returns a Registry pre-populated with the four built-in matchers.
func NewRegistry() *Registry {
	r := &Registry{m: map[string]Matcher{}}
	r.Register("equals", Equals{})
	r.Register("count_eq", CountEq{})
	r.Register("matches_shape", MatchesShape{})
	r.Register("contains", Contains{})
	return r
}

// Register adds or replaces a named matcher.
func (r *Registry) Register(name string, m Matcher) { r.m[name] = m }

// Get retrieves a matcher by name, returning an error if not found.
func (r *Registry) Get(name string) (Matcher, error) {
	m, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("matchers: unknown matcher %q", name)
	}
	return m, nil
}
