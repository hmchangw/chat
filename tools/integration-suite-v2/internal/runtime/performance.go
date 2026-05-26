package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// PerformanceStore is the on-disk record of pass-time history per
// (scenario_id, test_id). Persists across runs so the report can show
// latest / best / worst pass alongside today's run. Add entries when a
// new scenario passes; remove entries when their scenario is removed
// from the run set entirely.
type PerformanceStore struct {
	Version   int                     `json:"version"`
	Scenarios map[string]ScenarioPerf `json:"scenarios"`
}

// ScenarioPerf groups the tests within one scenario. Today every
// scenario has one test (subset "happy"); mishap expansion will add
// more.
type ScenarioPerf struct {
	Tests map[string]TestPerf `json:"tests"`
}

// TestPerf is one test's pass-time history. Steps mirrors the
// scenario's sequence in order: each entry tracks the per-service
// duration history so the report can show which service is the
// bottleneck and how that bottleneck is moving over time. Step
// history is reset for an index when the service at that index
// changes (sequence rewritten) — there's no meaningful continuity to
// preserve across a sequence change.
type TestPerf struct {
	LatestPass *PassRecord `json:"latest_pass,omitempty"`
	BestPass   *PassRecord `json:"best_pass,omitempty"`
	WorstPass  *PassRecord `json:"worst_pass,omitempty"`
	Steps      []StepPerf  `json:"steps,omitempty"`
}

// StepPerf is one sequence-step's pass-time history.
type StepPerf struct {
	Service    string      `json:"service"`
	LatestPass *PassRecord `json:"latest_pass,omitempty"`
	BestPass   *PassRecord `json:"best_pass,omitempty"`
	WorstPass  *PassRecord `json:"worst_pass,omitempty"`
}

// PassRecord pins one passing run.
type PassRecord struct {
	Commit     string `json:"commit"`
	DurationMs int64  `json:"duration_ms"`
	Timestamp  string `json:"timestamp"`
}

// LoadPerformance reads the store from disk; returns an empty store
// when the file doesn't exist yet (first run).
func LoadPerformance(path string) (*PerformanceStore, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &PerformanceStore{Version: 1, Scenarios: map[string]ScenarioPerf{}}, nil
		}
		return nil, fmt.Errorf("read performance store: %w", err)
	}
	var s PerformanceStore
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decode performance store: %w", err)
	}
	if s.Scenarios == nil {
		s.Scenarios = map[string]ScenarioPerf{}
	}
	if s.Version == 0 {
		s.Version = 1
	}
	return &s, nil
}

// SavePerformance writes the store back to disk, creating parent dirs.
func SavePerformance(path string, s *PerformanceStore) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode performance store: %w", err)
	}
	return os.WriteFile(path, b, 0o644)
}

// Update merges this run's results into the store: every PASSED test
// updates its latest_pass; best/worst are min/max'd against existing
// records. Failed tests don't update pass history. Scenarios present
// in the run but not in the store get added; scenarios in the store
// but no longer in the run are pruned (test-set is also pruned to
// match what's actually run). `commit` and `timestamp` are tagged on
// every pass updated in this run.
func (s *PerformanceStore) Update(cases []CaseReport, commit, timestamp string) {
	if s.Scenarios == nil {
		s.Scenarios = map[string]ScenarioPerf{}
	}
	currentScenarios := map[string]map[string]struct{}{}
	for ci := range cases {
		c := &cases[ci]
		if _, ok := currentScenarios[c.ScenarioName]; !ok {
			currentScenarios[c.ScenarioName] = map[string]struct{}{}
		}
		currentScenarios[c.ScenarioName][c.Subset] = struct{}{}

		if c.Verdict.Outcome != "pass" {
			continue
		}
		sp, ok := s.Scenarios[c.ScenarioName]
		if !ok {
			sp = ScenarioPerf{Tests: map[string]TestPerf{}}
		}
		if sp.Tests == nil {
			sp.Tests = map[string]TestPerf{}
		}
		tp := sp.Tests[c.Subset]
		rec := &PassRecord{Commit: commit, DurationMs: c.Duration.Milliseconds(), Timestamp: timestamp}
		tp.LatestPass = rec
		if tp.BestPass == nil || rec.DurationMs < tp.BestPass.DurationMs {
			tp.BestPass = rec
		}
		if tp.WorstPass == nil || rec.DurationMs > tp.WorstPass.DurationMs {
			tp.WorstPass = rec
		}
		// Per-step history: walk this case's steps in order, aligning
		// against the stored slice by index+service. Mismatched service at
		// an index resets that step's history (sequence rewritten).
		newSteps := make([]StepPerf, len(c.Verdict.Steps))
		for i, st := range c.Verdict.Steps {
			var sp_ StepPerf
			if i < len(tp.Steps) && tp.Steps[i].Service == st.Service {
				sp_ = tp.Steps[i]
			} else {
				sp_ = StepPerf{Service: st.Service}
			}
			if st.Duration > 0 {
				srec := &PassRecord{Commit: commit, DurationMs: st.Duration.Milliseconds(), Timestamp: timestamp}
				sp_.LatestPass = srec
				if sp_.BestPass == nil || srec.DurationMs < sp_.BestPass.DurationMs {
					sp_.BestPass = srec
				}
				if sp_.WorstPass == nil || srec.DurationMs > sp_.WorstPass.DurationMs {
					sp_.WorstPass = srec
				}
			}
			newSteps[i] = sp_
		}
		tp.Steps = newSteps
		sp.Tests[c.Subset] = tp
		s.Scenarios[c.ScenarioName] = sp
	}
	// Prune scenarios/tests no longer present in the run set.
	for scenName, sp := range s.Scenarios {
		curTests, scenStillRun := currentScenarios[scenName]
		if !scenStillRun {
			delete(s.Scenarios, scenName)
			continue
		}
		for testName := range sp.Tests {
			if _, ok := curTests[testName]; !ok {
				delete(sp.Tests, testName)
			}
		}
		if len(sp.Tests) == 0 {
			delete(s.Scenarios, scenName)
		} else {
			s.Scenarios[scenName] = sp
		}
	}
}
