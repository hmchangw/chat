// Package roomkeystore stores room encryption key pairs in Valkey.
//
// # Versioning
//
// Set assigns version 0 to a fresh key. Rotate increments version and demotes the
// current key into a per-room "previous" slot (room:<id>:key:prev) with a grace TTL.
// GetByVersion serves either the current or previous slot, enabling decrypt of
// messages encrypted under a recently-rotated key.
//
// # Concurrency
//
// Rotate is atomic via a single Lua script. Concurrent Rotate calls for the same
// room serialize at the Valkey server. Set and Get are not coordinated; readers
// see Set's write atomically once HSET completes.
//
// # Topology requirement
//
// Single Valkey master per site. The Lua rotate script does not work across
// Redis Cluster slots (room:<id>:key and room:<id>:key:prev are not hash-tagged).
// Sentinel + single-master is fine.
//
// # Federation
//
// This package is site-local. Cross-site replication is the responsibility of
// inbox-worker via chat.server.request.roomkey.<siteID>.get RPC.
package roomkeystore
