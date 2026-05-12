//go:build e2e

// Package harness provides the e2e test stack lifecycle, per-site clients,
// federation wiring, and per-test resource minting.
//
// # Which helper do I use?
//
// Several harness methods come in pairs where the right choice is non-
// obvious. This is the canonical answer; per-method godoc has details.
//
//	┌─────────────────────────────────────────────────────────────────┐
//	│ I want to ...                  │ Use ...                        │
//	├─────────────────────────────────────────────────────────────────┤
//	│ Authenticate as a realm user   │ Authenticate                   │
//	│   (default for tests)          │                                │
//	│                                │                                │
//	│ Authenticate from a non-test   │ AuthenticateE                  │
//	│   goroutine (load tests, etc.) │   (returns error vs t.Fatal;   │
//	│                                │    t.FailNow off a worker      │
//	│                                │    goroutine is UB)            │
//	├─────────────────────────────────────────────────────────────────┤
//	│ Mint a fresh per-test user     │ MintEphemeralUser              │
//	│   (most parallel tests)        │   (returns a random name)      │
//	│                                │                                │
//	│ Mint with an EXPLICIT name on  │ MintEphemeralUserAs            │
//	│   THIS site (typically to      │   (takes the username already  │
//	│   match a sibling site for     │    minted on the peer site;    │
//	│   federation tests)            │    federation keys off the     │
//	│                                │    account string, not the     │
//	│                                │    Keycloak user-id)           │
//	├─────────────────────────────────────────────────────────────────┤
//	│ Wait for a message to be in    │ awaitMessageOnSite             │
//	│   Cassandra under t.Parallel   │   (in helpers_test.go;         │
//	│                                │    polls messages_by_id by     │
//	│                                │    msg_id -- parallel-safe)    │
//	│                                │                                │
//	│ Wait for a worker to ack a     │ awaitCanonicalAcked            │
//	│   specific seq (only safe      │   (UNSAFE under t.Parallel on  │
//	│   when serial)                 │    a shared durable -- a       │
//	│                                │    sibling test's ack can      │
//	│                                │    satisfy your wait)          │
//	├─────────────────────────────────────────────────────────────────┤
//	│ Register a cleanup hook on     │ registerRoomCleanup(t,         │
//	│   the room's per-backend state │     []SiteDB{asSiteDB(t,       │
//	│   (every test creating a room) │         site)}, rid)           │
//	│                                │   (asSiteDB populates ALL      │
//	│                                │    fields; a bare `{SiteID,    │
//	│                                │    DB}` literal silently       │
//	│                                │    skips Cassandra/ES/Valkey   │
//	│                                │    cleanup -- DX footgun)      │
//	├─────────────────────────────────────────────────────────────────┤
//	│ Cross-site cleanup             │ []SiteDB{                      │
//	│   (federation tests)           │     asSiteDB(t, stack.SiteA),  │
//	│                                │     asSiteDB(t, stack.SiteB),  │
//	│                                │   }                            │
//	└─────────────────────────────────────────────────────────────────┘
//
// # Seed helpers vs production paths
//
// SeedRemoteUser, SeedUserRoom, and SeedRoomKey bypass production
// data-replication paths (INBOX user-replication, search-sync-worker's
// ES write, room-key distribution) so a test can set up state that's
// "as if" the real production path had run.
//
// Deliberate trade-off: makes single-component tests tractable (no need
// to drive a full federation flow just to authorize a search), but the
// real production paths don't get exercised by tests using the seeds.
// For full coverage, write a test that drives the real flow (e.g. a
// member_added OUTBOX event → search-sync-worker ES write); for
// everything else, the seeds are fine.
//
// See e2e/doc.go for the suite-level overview.
package harness
