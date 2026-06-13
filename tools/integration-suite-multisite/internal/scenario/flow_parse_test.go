package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParseFlow_Sequential(t *testing.T) {
	plan, err := ParseFlow(&FlowExpression{Raw: "a >> b >> c"})
	require.NoError(t, err)
	require.Len(t, plan.Barriers, 3)
	assert.Equal(t, []string{"a"}, plan.Barriers[0].IDs)
	assert.Equal(t, []string{"b"}, plan.Barriers[1].IDs)
	assert.Equal(t, []string{"c"}, plan.Barriers[2].IDs)
}

func TestParseFlow_ParallelGroup(t *testing.T) {
	plan, err := ParseFlow(&FlowExpression{Raw: "a >> [b, c] >> d"})
	require.NoError(t, err)
	require.Len(t, plan.Barriers, 3)
	assert.Equal(t, []string{"a"}, plan.Barriers[0].IDs)
	assert.Equal(t, []string{"b", "c"}, plan.Barriers[1].IDs)
	assert.Equal(t, []string{"d"}, plan.Barriers[2].IDs)
}

func TestParseFlow_SingleStep(t *testing.T) {
	plan, err := ParseFlow(&FlowExpression{Raw: "a"})
	require.NoError(t, err)
	require.Len(t, plan.Barriers, 1)
	assert.Equal(t, []string{"a"}, plan.Barriers[0].IDs)
}

func TestParseFlow_YAMLListEquivalent(t *testing.T) {
	// Compact and YAML-list forms must normalize to the same FlowPlan.
	compact := &FlowExpression{}
	require.NoError(t, yaml.Unmarshal([]byte(`a >> [b, c] >> d`), compact))

	list := &FlowExpression{}
	require.NoError(t, yaml.Unmarshal([]byte("- a\n- [b, c]\n- d\n"), list))

	planCompact, err := ParseFlow(compact)
	require.NoError(t, err)
	planList, err := ParseFlow(list)
	require.NoError(t, err)
	assert.Equal(t, planCompact, planList)
}

func TestParseFlow_NestedBrackets_Rejected(t *testing.T) {
	_, err := ParseFlow(&FlowExpression{Raw: "a >> [b, [c, d]]"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested brackets")
}

func TestParseFlow_EmptyGroup_Rejected(t *testing.T) {
	_, err := ParseFlow(&FlowExpression{Raw: "a >> [] >> b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestParseFlow_EmptyFlow_Rejected(t *testing.T) {
	_, err := ParseFlow(&FlowExpression{Raw: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestParseFlow_MalformedDoubleArrow_Rejected(t *testing.T) {
	_, err := ParseFlow(&FlowExpression{Raw: "a >> >> b"})
	require.Error(t, err)
}

func TestParseFlow_MalformedDoubleComma_Rejected(t *testing.T) {
	_, err := ParseFlow(&FlowExpression{Raw: "a >> [b,, c]"})
	require.Error(t, err)
}

func TestParseFlow_TrailingArrow_Rejected(t *testing.T) {
	_, err := ParseFlow(&FlowExpression{Raw: "a >> b >>"})
	require.Error(t, err)
}

func TestParseFlow_WhitespaceAgnostic(t *testing.T) {
	tight, err := ParseFlow(&FlowExpression{Raw: "a>>[b,c]>>d"})
	require.NoError(t, err)
	loose, err := ParseFlow(&FlowExpression{Raw: "  a  >>  [ b , c ]  >>  d  "})
	require.NoError(t, err)
	assert.Equal(t, tight, loose)
}

func TestParseFlow_BarrierKindDefaultsUnknown(t *testing.T) {
	// The parser doesn't know whether an id refers to an input (fire)
	// or expected (observe) — that's the validator's job. Barrier.Kind
	// is left empty by the parser.
	plan, err := ParseFlow(&FlowExpression{Raw: "a >> b"})
	require.NoError(t, err)
	for _, b := range plan.Barriers {
		assert.Empty(t, b.Kind, "parser leaves Kind empty; validator fills it after id-kind resolution")
	}
}

func TestParseFlow_UnbalancedBracketOpen_Rejected(t *testing.T) {
	_, err := ParseFlow(&FlowExpression{Raw: "a >> [b, c"})
	require.Error(t, err)
}

func TestParseFlow_UnbalancedBracketClose_Rejected(t *testing.T) {
	_, err := ParseFlow(&FlowExpression{Raw: "a >> b, c]"})
	require.Error(t, err)
}
