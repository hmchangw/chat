package main

import "errors"

// ErrInvalidRate is returned by every generator's Run() when Rate <= 0.
// CLAUDE.md §3 disallows bare-string fmt.Errorf returns; sentinels are
// the right alternative when there's no underlying error to wrap.
var ErrInvalidRate = errors.New("rate must be > 0")

// ErrUnknownHistoryKind is wrapped by buildHistoryRequest when the
// caller-supplied kind doesn't match a known historyRequestKind.
var ErrUnknownHistoryKind = errors.New("unknown historyRequestKind")

// ErrUnknownSearchKind is wrapped by buildSearchRequest when the
// caller-supplied kind doesn't match a known searchRequestKind.
var ErrUnknownSearchKind = errors.New("unknown searchRequestKind")

// ErrUnknownRoomKind is wrapped by buildRoomRequest when the
// caller-supplied kind doesn't match a known roomRequestKind.
var ErrUnknownRoomKind = errors.New("unknown roomRequestKind")
