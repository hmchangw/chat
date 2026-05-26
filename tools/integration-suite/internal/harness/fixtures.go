package harness

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// IDPrefixer generates scenario-scoped IDs that carry a stable prefix:
//
//	it-<runID>-<scenarioID>-<entity>
//
// All scenario data uses these so it never collides with real data
// and can be cleaned up by `make integration-suite-purge`.
type IDPrefixer struct {
	runID      string
	scenarioID string
}

// NewIDPrefixer creates a prefixer for one scenario in one run.
func NewIDPrefixer(runID, scenarioID string) *IDPrefixer {
	return &IDPrefixer{runID: runID, scenarioID: scenarioID}
}

// ID returns "it-<runID>-<scenarioID>-<entity>".
func (p *IDPrefixer) ID(entity string) string {
	return fmt.Sprintf("it-%s-%s-%s", p.runID, p.scenarioID, entity)
}

// GenerateRunID returns 4 hex chars. Collision-tolerant: 16^4 = 65,536
// distinct values is sufficient for human-distinguishable run labels.
func GenerateRunID() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("fixtures: GenerateRunID: %v", err))
	}
	return hex.EncodeToString(b[:])
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// ScenarioIDFromName converts a Gherkin scenario name to a kebab-case slug.
// "Adding a Member" → "adding-a-member".
func ScenarioIDFromName(name string) string {
	lower := strings.ToLower(name)
	dashed := nonAlnum.ReplaceAllString(lower, "-")
	return strings.Trim(dashed, "-")
}
