// Package roomkeystore stores room encryption key pairs in Valkey.
//
// # Versioning
//
// Set assigns version 0 to a fresh key. Rotate increments version and demotes the
// current key into a per-room "previous" slot (room:{<id>}:key:prev) with a grace
// TTL. GetByVersion serves either the current or previous slot, enabling decrypt
// of messages encrypted under a recently-rotated key.
//
// # Concurrency
//
// Rotate is atomic via a single Lua script. Concurrent Rotate calls for the same
// room serialize at the Valkey server. Set and Get are not coordinated; readers
// see Set's write atomically once HSET completes.
//
// # Topology
//
// Single-node, Sentinel, and Valkey/Redis Cluster are all supported. The
// underlying client is go-redis's UniversalClient, which picks the mode
// from the seed list passed via VALKEY_ADDRS — one address selects the
// single-node client, multiple addresses select the cluster client.
//
// Cluster correctness relies on a {roomID} hash tag in the key namespace
// (room:{<id>}:key and room:{<id>}:key:prev). The tag forces both slots
// for a room onto the same cluster shard so the atomic Lua rotate script
// and the two-key Delete pipeline never trip CROSSSLOT.
//
// # Federation
//
// Site-local only. A room exists on its origin site, so the broadcast pipeline
// that needs the key runs on that same site and reads from the origin's local
// keystore. There is no cross-site key replication; inbox-worker on remote
// sites replicates subscription/room metadata but never room keys.
package roomkeystore
