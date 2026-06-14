package pollers

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/msgbucket"
)

func TestDecodeJSONRow_HappyPath(t *testing.T) {
	got, err := decodeJSONRow(`{"room_id":"r-1","bucket":1700000000000,"msg":"hello"}`)
	require.NoError(t, err)
	assert.Equal(t, "r-1", got["room_id"])
	assert.Equal(t, float64(1700000000000), got["bucket"]) // JSON numbers → float64
	assert.Equal(t, "hello", got["msg"])
}

func TestDecodeJSONRow_EmptyRejected(t *testing.T) {
	_, err := decodeJSONRow("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestDecodeJSONRow_MalformedJSON(t *testing.T) {
	_, err := decodeJSONRow("{not json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// TestCassandraSelectPoller_BucketAtMatchesProductionSizer regresses
// the bucket-math contract: the poller's Sizer must produce identical
// values to the production msgbucket.Sizer the message-worker uses.
// Drift here would make scenario assertions race the producer's
// partition selection.
func TestCassandraSelectPoller_BucketAtMatchesProductionSizer(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	p := NewCassandraSelectPoller(nil, time.Now(), sizer)

	at := time.Date(2026, 5, 31, 12, 34, 56, 0, time.UTC)
	want := sizer.Of(at)
	got := p.BucketAt(at)
	assert.Equal(t, want, got)
	assert.Greater(t, got, int64(0))
}

// TestCassandraSelectPoller_MissingQueryReturnsNil — Phase 4.0
// universal primitive: missing args.query surfaces as slog warn +
// nil, never panics.
func TestCassandraSelectPoller_MissingQueryReturnsNil(t *testing.T) {
	p := NewCassandraSelectPoller(nil, time.Now(), msgbucket.New(24*time.Hour))
	events := p.PollFn("", map[string]any{}, "")()
	assert.Nil(t, events)
}

// TestBuildCassandraParams_DefaultsStartTimeForSingleQuestionMark —
// regresses the convention that the common "WHERE created_at >= ?"
// pattern auto-binds startTime when args.params is absent.
func TestBuildCassandraParams_DefaultsStartTimeForSingleQuestionMark(t *testing.T) {
	startTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := buildCassandraParams(map[string]any{}, startTime, "SELECT * FROM t WHERE created_at >= ?")
	require.Len(t, got, 1)
	assert.Equal(t, startTime, got[0])
}

// TestBuildCassandraParams_NoQuestionMarksNoBind — queries without
// placeholders MUST NOT bind a phantom startTime (would break
// constant queries like "SELECT count(*) FROM t").
func TestBuildCassandraParams_NoQuestionMarksNoBind(t *testing.T) {
	got := buildCassandraParams(map[string]any{}, time.Now(), "SELECT count(*) FROM t")
	assert.Nil(t, got)
}

// TestBuildCassandraParams_ExplicitParamsWin — author-supplied
// args.params overrides the default startTime binding.
func TestBuildCassandraParams_ExplicitParamsWin(t *testing.T) {
	got := buildCassandraParams(map[string]any{"params": []any{"r-1", "bucket-1"}}, time.Now(), "SELECT * FROM t WHERE room_id = ? AND bucket = ?")
	require.Len(t, got, 2)
	assert.Equal(t, "r-1", got[0])
	assert.Equal(t, "bucket-1", got[1])
}

// TestBuildCassandraParams_MultipleQuestionMarksNoDefaultBind — a
// multi-placeholder query without explicit args.params returns nil
// because there's no sensible single-value default. The poller's
// SELECT will then error out at gocql binding, which surfaces as a
// slog warning + empty result — a loud-fail design choice.
func TestBuildCassandraParams_MultipleQuestionMarksNoDefaultBind(t *testing.T) {
	got := buildCassandraParams(map[string]any{}, time.Now(), "SELECT * FROM t WHERE a = ? AND b = ?")
	assert.Nil(t, got, "ambiguous bind context → caller must supply params explicitly")
	_ = strings.Count
}

// TestTruncateForLog_NoTruncationWhenShort — preserves short rows as-is
// so the substrate-error warning carries the raw row when it fits.
func TestTruncateForLog_NoTruncationWhenShort(t *testing.T) {
	got := truncateForLog("hello", 200)
	assert.Equal(t, "hello", got)
}

// TestTruncateForLog_TruncatesWithMoreSuffix — the substrate-error
// row_prefix field is bounded; a 1 MiB malformed row must not flood
// the structured log. Suffix shows how much was dropped so the
// operator knows it was a giant blob, not a 200-byte parse error.
func TestTruncateForLog_TruncatesWithMoreSuffix(t *testing.T) {
	got := truncateForLog(strings.Repeat("x", 250), 200)
	assert.True(t, strings.HasPrefix(got, strings.Repeat("x", 200)))
	assert.Contains(t, got, "50 more bytes")
}

// --- P5: timestamp-typed param binding ---
// CQL `timestamp` columns need a Go time.Time bound at the ? — an
// int64 millis lands as a bigint and the comparison silently fails.
// Authors can opt in by writing a "ts:<unix-millis>" string in args.params,
// and buildCassandraParams converts it to time.Time before binding.

func TestBuildCassandraParams_TimestampStringConvertsToTime(t *testing.T) {
	// Unix millis 1748736000000 = 2025-06-01T00:00:00Z
	got := buildCassandraParams(
		map[string]any{"params": []any{"ts:1748736000000", "m-1"}},
		time.Now(),
		"SELECT * FROM messages_by_id WHERE created_at = ? AND message_id = ?",
	)
	require.Len(t, got, 2)
	tt, ok := got[0].(time.Time)
	require.True(t, ok, "ts:<millis> must convert to time.Time, got %T", got[0])
	assert.Equal(t, int64(1748736000000), tt.UnixMilli())
	assert.Equal(t, "m-1", got[1], "non-ts: params pass through unchanged")
}

func TestBuildCassandraParams_TimestampStringRejectsMalformed(t *testing.T) {
	// "ts:" with non-numeric body falls through as the raw string —
	// gocql will error at bind time with a precise type-mismatch.
	// We don't try to "rescue" it; that would mask the typo.
	got := buildCassandraParams(
		map[string]any{"params": []any{"ts:not-a-number"}},
		time.Now(),
		"SELECT 1",
	)
	require.Len(t, got, 1)
	assert.Equal(t, "ts:not-a-number", got[0], "malformed ts: stays a string so gocql surfaces the error loudly")
}

func TestBuildCassandraParams_NonTSPrefixedParams_Unchanged(t *testing.T) {
	// Regression guard: pure strings without ts: prefix continue to bind
	// as today (no implicit conversion magic).
	got := buildCassandraParams(
		map[string]any{"params": []any{"r-1", "01970a4f", int64(42)}},
		time.Now(),
		"SELECT * FROM t WHERE room_id = ? AND id = ? AND n = ?",
	)
	require.Len(t, got, 3)
	assert.Equal(t, "r-1", got[0])
	assert.Equal(t, "01970a4f", got[1])
	assert.Equal(t, int64(42), got[2])
}
