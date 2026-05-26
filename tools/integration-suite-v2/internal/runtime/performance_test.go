package runtime

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerformance_LoadMissingFileReturnsEmpty(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "performance.json")
	s, err := LoadPerformance(tmp)
	require.NoError(t, err)
	assert.Equal(t, 1, s.Version)
	assert.NotNil(t, s.Scenarios)
	assert.Empty(t, s.Scenarios)
}

func TestPerformance_UpdateAddsLatestBestWorst(t *testing.T) {
	s := &PerformanceStore{Version: 1, Scenarios: map[string]ScenarioPerf{}}
	cases := []CaseReport{
		{ScenarioName: "a", Subset: "happy", Duration: 100 * time.Millisecond, Verdict: Verdict{Outcome: "pass"}},
	}
	s.Update(cases, "abc1234", "2026-05-24T21:00:00Z")

	tp := s.Scenarios["a"].Tests["happy"]
	require.NotNil(t, tp.LatestPass)
	require.NotNil(t, tp.BestPass)
	require.NotNil(t, tp.WorstPass)
	assert.Equal(t, int64(100), tp.LatestPass.DurationMs)
	assert.Equal(t, "abc1234", tp.LatestPass.Commit)
}

func TestPerformance_UpdateBestWorstNarrow(t *testing.T) {
	s := &PerformanceStore{Version: 1, Scenarios: map[string]ScenarioPerf{}}
	s.Update([]CaseReport{
		{ScenarioName: "a", Subset: "happy", Duration: 100 * time.Millisecond, Verdict: Verdict{Outcome: "pass"}},
	}, "first", "t1")
	s.Update([]CaseReport{
		{ScenarioName: "a", Subset: "happy", Duration: 200 * time.Millisecond, Verdict: Verdict{Outcome: "pass"}},
	}, "slower", "t2")
	s.Update([]CaseReport{
		{ScenarioName: "a", Subset: "happy", Duration: 50 * time.Millisecond, Verdict: Verdict{Outcome: "pass"}},
	}, "fastest", "t3")

	tp := s.Scenarios["a"].Tests["happy"]
	assert.Equal(t, int64(50), tp.LatestPass.DurationMs, "latest tracks most recent")
	assert.Equal(t, "fastest", tp.LatestPass.Commit)
	assert.Equal(t, int64(50), tp.BestPass.DurationMs)
	assert.Equal(t, "fastest", tp.BestPass.Commit)
	assert.Equal(t, int64(200), tp.WorstPass.DurationMs)
	assert.Equal(t, "slower", tp.WorstPass.Commit)
}

func TestPerformance_UpdateFailedDoesNotChangePassHistory(t *testing.T) {
	s := &PerformanceStore{Version: 1, Scenarios: map[string]ScenarioPerf{}}
	s.Update([]CaseReport{
		{ScenarioName: "a", Subset: "happy", Duration: 100 * time.Millisecond, Verdict: Verdict{Outcome: "pass"}},
	}, "good", "t1")
	s.Update([]CaseReport{
		{ScenarioName: "a", Subset: "happy", Duration: 999 * time.Millisecond, Verdict: Verdict{Outcome: "fail"}},
	}, "bad", "t2")

	tp := s.Scenarios["a"].Tests["happy"]
	assert.Equal(t, int64(100), tp.LatestPass.DurationMs, "failed run should not change latest_pass")
	assert.Equal(t, "good", tp.LatestPass.Commit)
}

func TestPerformance_UpdatePrunesRemovedScenarios(t *testing.T) {
	s := &PerformanceStore{Version: 1, Scenarios: map[string]ScenarioPerf{
		"removed": {Tests: map[string]TestPerf{
			"happy": {LatestPass: &PassRecord{Commit: "old", DurationMs: 1}},
		}},
	}}
	s.Update([]CaseReport{
		{ScenarioName: "still_here", Subset: "happy", Duration: 100 * time.Millisecond, Verdict: Verdict{Outcome: "pass"}},
	}, "commit", "t1")

	_, removed := s.Scenarios["removed"]
	assert.False(t, removed, "scenario absent from current run set should be pruned")
	_, present := s.Scenarios["still_here"]
	assert.True(t, present)
}

func TestPerformance_RoundTrip(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "performance.json")
	s := &PerformanceStore{Version: 1, Scenarios: map[string]ScenarioPerf{}}
	s.Update([]CaseReport{
		{ScenarioName: "x", Subset: "happy", Duration: 50 * time.Millisecond, Verdict: Verdict{Outcome: "pass"}},
	}, "abc", "t1")
	require.NoError(t, SavePerformance(tmp, s))
	require.FileExists(t, tmp)

	loaded, err := LoadPerformance(tmp)
	require.NoError(t, err)
	tp := loaded.Scenarios["x"].Tests["happy"]
	require.NotNil(t, tp.LatestPass)
	assert.Equal(t, "abc", tp.LatestPass.Commit)
	assert.Equal(t, int64(50), tp.LatestPass.DurationMs)
}

// TestPerformance_PerStepHistory confirms step-level latest/best/worst
// tracks each service independently and survives serialisation.
func TestPerformance_PerStepHistory(t *testing.T) {
	s := &PerformanceStore{Version: 1, Scenarios: map[string]ScenarioPerf{}}
	mkCase := func(svc1, svc2 time.Duration) CaseReport {
		return CaseReport{
			ScenarioName: "p",
			Subset:       "happy",
			Duration:     svc1 + svc2,
			Verdict: Verdict{
				Outcome: "pass",
				Steps: []StepResult{
					{Service: "room-service", Outcome: "pass", Duration: svc1},
					{Service: "room-worker", Outcome: "pass", Duration: svc2},
				},
			},
		}
	}
	s.Update([]CaseReport{mkCase(50*time.Millisecond, 200*time.Millisecond)}, "c1", "t1")
	s.Update([]CaseReport{mkCase(60*time.Millisecond, 180*time.Millisecond)}, "c2", "t2")
	s.Update([]CaseReport{mkCase(40*time.Millisecond, 300*time.Millisecond)}, "c3", "t3")

	tp := s.Scenarios["p"].Tests["happy"]
	require.Len(t, tp.Steps, 2)

	// room-service: latest 40, best 40 (c3), worst 60 (c2)
	rs := tp.Steps[0]
	assert.Equal(t, "room-service", rs.Service)
	assert.Equal(t, int64(40), rs.LatestPass.DurationMs)
	assert.Equal(t, "c3", rs.LatestPass.Commit)
	assert.Equal(t, int64(40), rs.BestPass.DurationMs)
	assert.Equal(t, int64(60), rs.WorstPass.DurationMs)
	assert.Equal(t, "c2", rs.WorstPass.Commit)

	// room-worker: latest 300, best 180 (c2), worst 300 (c3)
	rw := tp.Steps[1]
	assert.Equal(t, "room-worker", rw.Service)
	assert.Equal(t, int64(300), rw.LatestPass.DurationMs)
	assert.Equal(t, int64(180), rw.BestPass.DurationMs)
	assert.Equal(t, "c2", rw.BestPass.Commit)
	assert.Equal(t, int64(300), rw.WorstPass.DurationMs)
}

// TestPerformance_PerStepResetsOnServiceChange confirms that when a
// scenario's sequence is rewritten (different service at the same
// index), that step's history is discarded — there's no meaningful
// continuity to keep.
func TestPerformance_PerStepResetsOnServiceChange(t *testing.T) {
	s := &PerformanceStore{Version: 1, Scenarios: map[string]ScenarioPerf{}}
	s.Update([]CaseReport{{
		ScenarioName: "p", Subset: "happy", Duration: 100 * time.Millisecond,
		Verdict: Verdict{Outcome: "pass", Steps: []StepResult{
			{Service: "room-service", Outcome: "pass", Duration: 100 * time.Millisecond},
		}},
	}}, "c1", "t1")
	s.Update([]CaseReport{{
		ScenarioName: "p", Subset: "happy", Duration: 50 * time.Millisecond,
		Verdict: Verdict{Outcome: "pass", Steps: []StepResult{
			{Service: "auth-service", Outcome: "pass", Duration: 50 * time.Millisecond},
		}},
	}}, "c2", "t2")

	tp := s.Scenarios["p"].Tests["happy"]
	require.Len(t, tp.Steps, 1)
	assert.Equal(t, "auth-service", tp.Steps[0].Service)
	assert.Equal(t, int64(50), tp.Steps[0].LatestPass.DurationMs, "history reset to the new service's run")
	assert.Equal(t, int64(50), tp.Steps[0].BestPass.DurationMs)
	assert.Equal(t, int64(50), tp.Steps[0].WorstPass.DurationMs)
}

func TestPerformance_SaveCreatesParentDir(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "nested", "deep", "performance.json")
	s := &PerformanceStore{Version: 1, Scenarios: map[string]ScenarioPerf{}}
	require.NoError(t, SavePerformance(tmp, s))
	_, err := os.Stat(tmp)
	require.NoError(t, err)
}
