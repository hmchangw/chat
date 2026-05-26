package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleRegister = `# Coverage Register

## history-thread-empty-id

**Service:** history
**Source:** specs/x.md § Error Matrix
**Behavior:** Empty threadMessageId is rejected.
**Status:** covered

## history-thread-not-subscribed

**Service:** history
**Source:** specs/x.md § Error Matrix
**Behavior:** Unsubscribed caller is forbidden.
**Status:** blindspot

## history-loadhistory-newest-first

**Source:** specs/y.md § Pagination
**Behavior:** History returns newest-first.
`

func TestParseCoverageRegister(t *testing.T) {
	entries := ParseCoverageRegister(sampleRegister)
	require.Len(t, entries, 3)

	assert.Equal(t, "history-thread-empty-id", entries[0].Slug)
	assert.Equal(t, "history", entries[0].Service)
	assert.Equal(t, "specs/x.md § Error Matrix", entries[0].Source)
	assert.Equal(t, "covered", entries[0].Declared)

	assert.Equal(t, "blindspot", entries[1].Declared)

	// No **Service:** and no **Status:** → service from slug prefix, status default.
	assert.Equal(t, "history", entries[2].Service)
	assert.Equal(t, "todo", entries[2].Declared)
}

func TestExtractCoversSlugs(t *testing.T) {
	txt := "@status:approved @covers:room-create-channel\n@covers:history-thread-empty-id @smoke"
	got := ExtractCoversSlugs(txt)
	assert.ElementsMatch(t, []string{"room-create-channel", "history-thread-empty-id"}, got)
}

func TestDiffCoverage(t *testing.T) {
	orphan, never := DiffCoverage(
		[]string{"a", "b"}, // referenced by features
		[]string{"b", "c"}, // declared in register
	)
	assert.Equal(t, []string{"a"}, orphan) // @covers:a not in register
	assert.Equal(t, []string{"c"}, never)  // c declared, never @covers'd
}

// cucumber JSON: one passing scenario covering empty-id, one blindspot
// scenario tagged @covers (must NOT count as covered).
const sampleCukeJSON = `[
 {"uri":"features/service/history.feature","elements":[
   {"id":"s1","name":"empty id","line":5,"type":"scenario",
    "tags":[{"name":"@covers:history-thread-empty-id"}],
    "steps":[{"name":"x","result":{"status":"passed"}}]},
   {"id":"s2","name":"not subscribed","line":12,"type":"scenario",
    "tags":[{"name":"@blindspot:history-forbidden-class-unverifiable"},
            {"name":"@covers:history-thread-not-subscribed"}],
    "steps":[{"name":"y","result":{"status":"failed","error_message":"blindspot"}}]}
 ]}
]`

func TestScoreCoverage(t *testing.T) {
	entries := ParseCoverageRegister(sampleRegister)
	rep, err := ScoreCoverage([]byte(sampleCukeJSON), entries)
	require.NoError(t, err)
	require.Len(t, rep.Services, 1)

	h := rep.Services[0]
	assert.Equal(t, "history", h.Service)
	assert.Equal(t, 1, h.Covered)   // empty-id passed
	assert.Equal(t, 1, h.KnownGap)  // not-subscribed declared blindspot (covers tag on a blindspot scenario does NOT cover)
	assert.Equal(t, 1, h.Uncovered) // loadhistory-newest-first: no scenario
	assert.Equal(t, 3, h.Total())
	assert.InDelta(t, 33.3, h.PctCovered(), 0.1)

	require.Len(t, rep.Uncovered, 1)
	assert.Equal(t, "history-loadhistory-newest-first", rep.Uncovered[0].Slug)
}

func TestScoreCoverageEmptyDoc(t *testing.T) {
	rep, err := ScoreCoverage(nil, ParseCoverageRegister(sampleRegister))
	require.NoError(t, err)
	// Nothing covered: empty-id -> uncovered, not-subscribed -> known gap.
	h := rep.Services[0]
	assert.Equal(t, 0, h.Covered)
	assert.Equal(t, 1, h.KnownGap)
	assert.Equal(t, 2, h.Uncovered)
}

func TestRenderCoverageEmpty(t *testing.T) {
	out := RenderCoverage(&CoverageReport{})
	assert.Contains(t, out, "no coverage.md register found")
}
