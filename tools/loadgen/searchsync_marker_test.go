//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/testutil"
)

// TestSearchSyncACLMarker_FreshAfterWrite verifies that a just-written marker
// reports fresh, satisfying the runRun auto-skip contract.
func TestSearchSyncACLMarker_FreshAfterWrite(t *testing.T) {
	db := testutil.MongoDB(t, "loadgen_acl_marker")
	ctx := context.Background()

	require.False(t, HasFreshSearchSyncACLMarker(ctx, db, "site-local"),
		"empty DB must not report a fresh marker")

	require.NoError(t, WriteSearchSyncACLMarker(ctx, db, "site-local"))
	assert.True(t, HasFreshSearchSyncACLMarker(ctx, db, "site-local"),
		"just-written marker must report fresh")
}

// TestSearchSyncACLMarker_StaleAfterWindow simulates a doc older than
// SearchSyncACLMarkerMaxAge by writing the doc directly with a stale
// timestamp, and confirms the auto-skip refuses to trust it.
func TestSearchSyncACLMarker_StaleAfterWindow(t *testing.T) {
	db := testutil.MongoDB(t, "loadgen_acl_marker_stale")
	ctx := context.Background()

	stale := time.Now().UTC().Add(-(SearchSyncACLMarkerMaxAge + time.Hour))
	_, err := db.Collection(SearchSyncACLMarkerCollection).InsertOne(ctx, bson.M{
		"_id":            "site-local",
		"bootstrappedAt": stale,
	})
	require.NoError(t, err)

	assert.False(t, HasFreshSearchSyncACLMarker(ctx, db, "site-local"),
		"marker older than SearchSyncACLMarkerMaxAge must report not-fresh")
}

// TestSearchSyncACLMarker_PerSiteIsolation confirms the marker is keyed by
// siteID so a bootstrap for one site doesn't make another site's run skip
// its own bootstrap.
func TestSearchSyncACLMarker_PerSiteIsolation(t *testing.T) {
	db := testutil.MongoDB(t, "loadgen_acl_marker_persite")
	ctx := context.Background()

	require.NoError(t, WriteSearchSyncACLMarker(ctx, db, "site-a"))
	assert.True(t, HasFreshSearchSyncACLMarker(ctx, db, "site-a"))
	assert.False(t, HasFreshSearchSyncACLMarker(ctx, db, "site-b"),
		"site-b marker is independent of site-a marker")
}

// TestSearchSyncACLMarker_OverwriteUpdatesTimestamp confirms repeat writes
// refresh the timestamp (operator running `seed --with-search-sync-acl`
// multiple times shouldn't pin an old timestamp).
func TestSearchSyncACLMarker_OverwriteUpdatesTimestamp(t *testing.T) {
	db := testutil.MongoDB(t, "loadgen_acl_marker_overwrite")
	ctx := context.Background()

	require.NoError(t, WriteSearchSyncACLMarker(ctx, db, "site-local"))
	var first searchSyncACLMarker
	require.NoError(t, db.Collection(SearchSyncACLMarkerCollection).
		FindOne(ctx, bson.M{"_id": "site-local"}).Decode(&first))

	time.Sleep(20 * time.Millisecond)
	require.NoError(t, WriteSearchSyncACLMarker(ctx, db, "site-local"))
	var second searchSyncACLMarker
	require.NoError(t, db.Collection(SearchSyncACLMarkerCollection).
		FindOne(ctx, bson.M{"_id": "site-local"}).Decode(&second))

	assert.True(t, second.BootstrappedAt.After(first.BootstrappedAt),
		"second write must advance bootstrappedAt (operator re-seed must refresh the marker)")
}
