package cassrepo

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
)

// Bucket cursor wire format (then base64-encoded for transport):
//
//	[bucket: 8 bytes BE int64][pageStateLen: 2 bytes BE uint16][pageState: N bytes]
//
// Empty input string decodes to (bucket=0, pageState=nil), interpreted by the
// walker as "start from caller-supplied startBucket with a fresh in-bucket
// query". The bucket=0 sentinel is unambiguous because the walker passes its
// own startBucket when the cursor is empty.
const bucketCursorHeaderBytes = 8 + 2

func encodeBucketCursor(bucket int64, pageState []byte) string {
	buf := make([]byte, bucketCursorHeaderBytes+len(pageState))
	binary.BigEndian.PutUint64(buf[0:8], uint64(bucket))
	binary.BigEndian.PutUint16(buf[8:10], uint16(len(pageState)))
	copy(buf[bucketCursorHeaderBytes:], pageState)
	return base64.StdEncoding.EncodeToString(buf)
}

func decodeBucketCursor(encoded string) (int64, []byte, error) {
	if encoded == "" {
		return 0, nil, nil
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxCursorBytes) {
		return 0, nil, fmt.Errorf("decode bucket cursor: encoded length %d exceeds maximum", len(encoded))
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return 0, nil, fmt.Errorf("decode bucket cursor: %w", err)
	}
	if len(raw) > maxCursorBytes {
		return 0, nil, fmt.Errorf("decode bucket cursor: decoded length %d exceeds maximum %d", len(raw), maxCursorBytes)
	}
	if len(raw) < bucketCursorHeaderBytes {
		return 0, nil, fmt.Errorf("decode bucket cursor: truncated framing (%d bytes)", len(raw))
	}
	bucket := int64(binary.BigEndian.Uint64(raw[0:8]))
	psLen := int(binary.BigEndian.Uint16(raw[8:10]))
	if bucketCursorHeaderBytes+psLen > len(raw) {
		return 0, nil, fmt.Errorf("decode bucket cursor: pageState length %d exceeds available %d", psLen, len(raw)-bucketCursorHeaderBytes)
	}
	var pageState []byte
	if psLen > 0 {
		pageState = make([]byte, psLen)
		copy(pageState, raw[bucketCursorHeaderBytes:bucketCursorHeaderBytes+psLen])
	}
	return bucket, pageState, nil
}
