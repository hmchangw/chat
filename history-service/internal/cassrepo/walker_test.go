package cassrepo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBucketCursor_RoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		bucket    int64
		pageState []byte
	}{
		{name: "empty page state", bucket: 86_400_000, pageState: nil},
		{name: "small page state", bucket: 0, pageState: []byte{0x01, 0x02, 0x03}},
		{name: "negative bucket allowed (pre-epoch test data)", bucket: -86_400_000, pageState: []byte{0xff}},
		{name: "long page state", bucket: 1_700_000_000_000, pageState: make([]byte, 200)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encodeBucketCursor(tc.bucket, tc.pageState)
			require.NotEmpty(t, encoded, "encoded cursor must not be empty")

			gotBucket, gotPageState, err := decodeBucketCursor(encoded)
			require.NoError(t, err)
			assert.Equal(t, tc.bucket, gotBucket)
			assert.Equal(t, tc.pageState, gotPageState)
		})
	}
}

func TestBucketCursor_EmptyEncoded_IsFreshWalk(t *testing.T) {
	bucket, pageState, err := decodeBucketCursor("")
	require.NoError(t, err)
	assert.Equal(t, int64(0), bucket)
	assert.Nil(t, pageState)
}

func TestBucketCursor_RejectsOversize(t *testing.T) {
	big := make([]byte, maxCursorBytes+1)
	encoded := encodeBucketCursor(0, big)
	_, _, err := decodeBucketCursor(encoded)
	require.Error(t, err)
}

func TestBucketCursor_RejectsCorruptBase64(t *testing.T) {
	_, _, err := decodeBucketCursor("not-valid-base64!@#")
	require.Error(t, err)
}

func TestBucketCursor_RejectsTruncatedFraming(t *testing.T) {
	// Valid base64 but only 4 bytes (< 8-byte bucket header).
	encoded := encodeBucketCursor(0, nil)[:6]
	_, _, err := decodeBucketCursor(encoded)
	require.Error(t, err)
}
