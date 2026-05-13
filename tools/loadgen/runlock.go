package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// ErrConcurrentRun is returned when a fresh run refuses to start because
// another active loadgen row exists for the same SUT URL within the TTL
// window.
var ErrConcurrentRun = errors.New("another loadgen run is already active for this SUT")

// shortRunID returns the first 7 characters of runID for use in resource
// names (Mongo DB names, JetStream consumer names).  When runID is shorter
// than 7 chars the full string is returned.
func shortRunID(runID string) string {
	if len(runID) <= 7 {
		return runID
	}
	return runID[:7]
}

// mongoDBName returns the per-run Mongo database name: "loadgen_<short>".
// The "loadgen_" prefix satisfies the guardMongoDB isolation check so the
// generated name is always safe for seed/teardown operations.
func mongoDBName(runID string) string {
	return "loadgen_" + shortRunID(runID)
}

// consumerName returns the per-run JetStream durable consumer name:
// "loadgen_<short>_<purpose>".  Concurrent runs on the same machine each
// get their own durable so they don't share or steal each other's messages.
func consumerName(runID, purpose string) string {
	return "loadgen_" + shortRunID(runID) + "_" + purpose
}

// RunLock uses a MongoDB collection as a distributed advisory lock.
// Each Acquire inserts a row; Release deletes it. The lock is advisory —
// it is bypassed when allowConcurrent is true (two-machine campaigns).
type RunLock struct {
	coll   *mongo.Collection
	runID  string
	sutURL string
	ttl    time.Duration
}

// runLockDoc is the document stored in the loadgen_runs collection.
type runLockDoc struct {
	RunID     string    `bson:"runID"`
	StartedAt time.Time `bson:"startedAt"`
	Host      string    `bson:"host"`
	Scenario  string    `bson:"scenario"`
	SUTURL    string    `bson:"sutURL"`
}

// NewRunLock creates a RunLock backed by db.Collection("loadgen_runs").
// sutURL identifies the system under test; ttl is the max age of an active
// row before it is considered orphaned and ignored.
func NewRunLock(db *mongo.Database, sutURL string, ttl time.Duration) *RunLock {
	return &RunLock{
		coll:   db.Collection("loadgen_runs"),
		sutURL: sutURL,
		ttl:    ttl,
	}
}

// Acquire checks for an active run on the same SUT URL (unless
// allowConcurrent is true) and inserts a row for this run.
//
// Returns ErrConcurrentRun when a conflicting active row is found.
// Returns a wrapped error on any Mongo operation failure.
func (l *RunLock) Acquire(ctx context.Context, runID, scenario string, allowConcurrent bool) error {
	if !allowConcurrent {
		cutoff := time.Now().Add(-l.ttl)
		err := l.coll.FindOne(ctx, bson.M{
			"sutURL":    l.sutURL,
			"startedAt": bson.M{"$gt": cutoff},
		}).Err()
		switch {
		case err == nil:
			// A document was found — another run is active.
			return ErrConcurrentRun
		case errors.Is(err, mongo.ErrNoDocuments):
			// No active run; proceed to insert.
		default:
			return fmt.Errorf("query runlock: %w", err)
		}
	}
	return l.insertRow(ctx, runID, scenario)
}

// Release deletes the lock document for this run. Safe to call even if
// Acquire was never called successfully (delete of a nonexistent document
// is a no-op in terms of correctness; it will return an error only on
// driver-level failures).
func (l *RunLock) Release(ctx context.Context) error {
	if l.runID == "" {
		return nil // Acquire never succeeded; nothing to release.
	}
	_, err := l.coll.DeleteOne(ctx, bson.M{"runID": l.runID})
	if err != nil {
		return fmt.Errorf("release runlock: %w", err)
	}
	return nil
}

func (l *RunLock) insertRow(ctx context.Context, runID, scenario string) error {
	l.runID = runID
	host, _ := os.Hostname()
	_, err := l.coll.InsertOne(ctx, runLockDoc{
		RunID:     runID,
		StartedAt: time.Now().UTC(),
		Host:      host,
		Scenario:  scenario,
		SUTURL:    l.sutURL,
	})
	if err != nil {
		return fmt.Errorf("insert runlock: %w", err)
	}
	return nil
}
