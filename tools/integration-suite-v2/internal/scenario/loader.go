package scenario

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadFile reads a scenario YAML at the given path.
func LoadFile(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	if s.Name == "" {
		return nil, fmt.Errorf("scenario %s: missing `scenario:` name", path)
	}
	if s.Source == "" {
		return nil, fmt.Errorf("scenario %s: missing `source:` citation", path)
	}
	if s.Input.Verb == "" {
		return nil, fmt.Errorf("scenario %s: missing `input.verb`", path)
	}
	for i, m := range s.Mishaps.Ignore {
		if m.Reason == "" {
			return nil, fmt.Errorf("scenario %s: mishaps.ignore[%d] (%q) missing reason", path, i, m.Mishap)
		}
	}
	return &s, nil
}
