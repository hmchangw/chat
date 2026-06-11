package mongoutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewOffsetPageRequest_Defaults(t *testing.T) {
	p := NewOffsetPageRequest(0, 0)
	assert.Equal(t, int64(0), p.Offset)
	assert.Equal(t, int64(20), p.Limit)
}

func TestNewOffsetPageRequest_Custom(t *testing.T) {
	p := NewOffsetPageRequest(10, 30)
	assert.Equal(t, int64(10), p.Offset)
	assert.Equal(t, int64(30), p.Limit)
}

func TestNewOffsetPageRequest_LimitCapped(t *testing.T) {
	p := NewOffsetPageRequest(0, 200)
	assert.Equal(t, int64(100), p.Limit)
}

func TestNewOffsetPageRequest_NegativeOffset(t *testing.T) {
	p := NewOffsetPageRequest(-5, 20)
	assert.Equal(t, int64(0), p.Offset)
}

func TestNewOffsetPageRequest_NegativeLimit(t *testing.T) {
	p := NewOffsetPageRequest(0, -1)
	assert.Equal(t, int64(20), p.Limit)
}

func TestEmptyPage(t *testing.T) {
	page := EmptyPage[int]()
	assert.NotNil(t, page.Data)
	assert.Empty(t, page.Data)
	assert.Equal(t, int64(0), page.Total)
}

func TestNewOffsetPageRequestWithBounds_DefaultApplied(t *testing.T) {
	p := NewOffsetPageRequestWithBounds(0, 0, 40, 1000)
	assert.Equal(t, int64(0), p.Offset)
	assert.Equal(t, int64(40), p.Limit)
}

func TestNewOffsetPageRequestWithBounds_NegativeLimitDefaulted(t *testing.T) {
	p := NewOffsetPageRequestWithBounds(0, -7, 40, 1000)
	assert.Equal(t, int64(40), p.Limit)
}

func TestNewOffsetPageRequestWithBounds_InRangePassThrough(t *testing.T) {
	p := NewOffsetPageRequestWithBounds(80, 25, 40, 1000)
	assert.Equal(t, int64(80), p.Offset)
	assert.Equal(t, int64(25), p.Limit)
}

func TestNewOffsetPageRequestWithBounds_CapApplied(t *testing.T) {
	p := NewOffsetPageRequestWithBounds(0, 5000, 40, 1000)
	assert.Equal(t, int64(1000), p.Limit)
}

func TestNewOffsetPageRequestWithBounds_NegativeOffsetClamped(t *testing.T) {
	p := NewOffsetPageRequestWithBounds(-5, 40, 40, 1000)
	assert.Equal(t, int64(0), p.Offset)
}
