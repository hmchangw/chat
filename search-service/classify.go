package main

// classifyRoomIDs splits a user-supplied set of roomIDs into two buckets:
//   - unrestricted: roomIDs not present in the restricted map — the ES
//     terms-lookup in scopedAccessClauses is the final access enforcer for
//     this bucket.
//   - restrictedWithFloor: roomIDs present in the restricted map, keyed by
//     their HSS-millis value.
//
// Security contract: when the caller supplies a roomID that is in neither
// bucket, it is treated as unrestricted and gated by the ES terms-lookup.
// The ES layer silently drops rooms the caller does not belong to, so a
// malicious caller cannot probe arbitrary roomIDs by listing them here.
//
// When `requested` is nil or empty, both return values are nil — this
// signals to the caller to use the existing global-search path (all rooms
// via the ES user-room terms-lookup), preserving the pre-v2 behavior.
func classifyRoomIDs(requested []string, restricted map[string]int64) (unrestricted []string, restrictedWithFloor map[string]int64) {
	if len(requested) == 0 {
		return nil, nil
	}

	restrictedWithFloor = make(map[string]int64)
	unrestricted = make([]string, 0, len(requested))

	for _, rid := range requested {
		if floor, ok := restricted[rid]; ok {
			restrictedWithFloor[rid] = floor
		} else {
			unrestricted = append(unrestricted, rid)
		}
	}

	if len(restrictedWithFloor) == 0 {
		restrictedWithFloor = nil
	}
	return unrestricted, restrictedWithFloor
}
