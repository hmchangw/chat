package main

// searchsync_marker.go — tracks whether the seed-time user-room ACL bootstrap
// for the search-sync-lag scenario has been done recently enough that the
// per-Run bootstrap can be skipped automatically.
//
// Lives in the shared Mongo `loadgen_shared` DB (alongside the runlock) so a
// `loadgen seed --with-search-sync-acl` and a subsequent `loadgen run
// --scenario=search-sync-lag` can communicate without either side needing
// Valkey or extra env state. Each marker doc keys on siteID + records the
// timestamp of the seed-time bootstrap completion.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// SearchSyncACLMarkerCollection is the Mongo collection name (within
// SharedLockDBName) that holds one doc per siteID recording the most-recent
// successful seed-time ACL bootstrap.
const SearchSyncACLMarkerCollection = "search_sync_acl_markers"

// SearchSyncACLMarkerMaxAge bounds how long after a seed-time bootstrap we
// trust the marker enough to auto-skip the run-time bootstrap. After this
// window an ES index churn / refresh schedule change / sync-worker restart
// could have re-cleared the user-room doc, so we fall back to bootstrap
// rather than risk an empty histogram.
const SearchSyncACLMarkerMaxAge = 24 * time.Hour

// searchSyncACLMarker is the marker doc shape. SiteID is the primary key
// because a single Mongo can host fixtures for multiple sites concurrently.
type searchSyncACLMarker struct {
	SiteID         string    `bson:"_id"`
	BootstrappedAt time.Time `bson:"bootstrappedAt"`
}

// WriteSearchSyncACLMarker upserts the marker doc for siteID with the current
// time. Called from `dispatchSeedSearchSyncACL` after a successful bootstrap.
// The shared DB / collection is created on first write — no schema setup.
func WriteSearchSyncACLMarker(ctx context.Context, db *mongo.Database, siteID string) error {
	coll := db.Collection(SearchSyncACLMarkerCollection)
	filter := bson.M{"_id": siteID}
	update := bson.M{"$set": bson.M{"bootstrappedAt": time.Now().UTC()}}
	opts := options.UpdateOne().SetUpsert(true)
	if _, err := coll.UpdateOne(ctx, filter, update, opts); err != nil {
		return fmt.Errorf("upsert search-sync-acl marker for site=%s: %w", siteID, err)
	}
	return nil
}

// HasFreshSearchSyncACLMarker returns true when the marker doc for siteID
// exists AND its bootstrappedAt is within SearchSyncACLMarkerMaxAge. A
// missing doc, an absent shared DB, or a doc older than the window all
// return false (no error — auto-detect must never block the run; the
// run-time bootstrap is a safe fallback).
func HasFreshSearchSyncACLMarker(ctx context.Context, db *mongo.Database, siteID string) bool {
	coll := db.Collection(SearchSyncACLMarkerCollection)
	var m searchSyncACLMarker
	err := coll.FindOne(ctx, bson.M{"_id": siteID}).Decode(&m)
	if err != nil {
		// ErrNoDocuments is the common "not seeded" case; any other error
		// (Mongo unreachable, network glitch) also surfaces as "not fresh"
		// so the caller falls back to the in-Run bootstrap.
		if !errors.Is(err, mongo.ErrNoDocuments) {
			// Real errors are worth a log but not a hard fail.
			// The caller will log its own decision; we just return false.
			_ = err
		}
		return false
	}
	return time.Since(m.BootstrappedAt) <= SearchSyncACLMarkerMaxAge
}
