package mishap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPowerSet_EmptyInputYieldsSingleEmptySet(t *testing.T) {
	got := PowerSet([]string{})
	assert.Len(t, got, 1)
	assert.Empty(t, got[0])
}

func TestPowerSet_ThreeElementsYields8Subsets(t *testing.T) {
	got := PowerSet([]string{"a", "b", "c"})
	assert.Len(t, got, 8)
}

func TestExpand_AppliesIgnoreList(t *testing.T) {
	applicable := []string{"kill_pod", "concurrent", "partition"}
	ignore := []string{"partition"}
	subsets := Expand(applicable, ignore)
	// 2^2 = 4 subsets after filtering out partition
	assert.Len(t, subsets, 4)
}
