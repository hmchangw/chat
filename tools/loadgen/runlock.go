package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// ErrConcurrentRun is returned when a fresh run refuses to start because
// another active loadgen row exists for the same SUT URL within the TTL
// window.
var ErrConcurrentRun = errors.New("another loadgen run is already active for this SUT")

// SharedLockDBName is the shared Mongo database that holds the run-isolation
// lock collection (loadgen_runs). It must NOT be a per-run DB — concurrent
// runs in different per-run DBs would never see each other's lock rows.
// The "loadgen_" prefix satisfies guardMongoDB's isolation check.
const SharedLockDBName = "loadgen_shared"

// ConcurrentRunError is returned by Acquire when a conflicting active run is
// found. It carries enough detail for the operator to identify the blocking
// run. errors.Is(err, ErrConcurrentRun) returns true for this type.
type ConcurrentRunError struct {
	ExistingRunID     string
	ExistingHost      string
	ExistingScenario  string
	ExistingStartedAt time.Time
	SUTURL            string
}

// Error satisfies the error interface and returns a human-readable description
// of the conflicting run including its ID, host, scenario, and SUT URL.
func (e *ConcurrentRunError) Error() string {
	return fmt.Sprintf("another loadgen run %s is already active for SUT %q (host=%s scenario=%s started=%s)",
		e.ExistingRunID, e.SUTURL, e.ExistingHost, e.ExistingScenario, e.ExistingStartedAt.Format(time.RFC3339))
}

// Is makes errors.Is(err, ErrConcurrentRun) return true for *ConcurrentRunError.
func (e *ConcurrentRunError) Is(target error) bool {
	return target == ErrConcurrentRun
}

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

// NewRunLock creates a lock backed by the "loadgen_runs" collection in `db`.
// CRITICAL: `db` MUST be a SHARED database (typically loadgen_shared) — NOT
// a per-run database (loadgen_<short>). Concurrent runs in different per-run
// DBs would never see each other's lock rows. main.go uses SharedLockDBName.
func NewRunLock(db *mongo.Database, sutURL string, ttl time.Duration) *RunLock {
	coll := db.Collection("loadgen_runs")
	// Ensure the active-row query uses an index. Idempotent.
	_, err := coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{{Key: "sutURL", Value: 1}, {Key: "startedAt", Value: 1}},
	})
	if err != nil {
		slog.Debug("ensure index", "error", err)
	}
	return &RunLock{
		coll:   coll,
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
		res := l.coll.FindOne(ctx, bson.M{
			"sutURL":    l.sutURL,
			"startedAt": bson.M{"$gt": cutoff},
		})
		if err := res.Err(); err == nil {
			// A document was found — another run is active.
			var existing runLockDoc
			if decErr := res.Decode(&existing); decErr == nil {
				return &ConcurrentRunError{
					ExistingRunID:     existing.RunID,
					ExistingHost:      existing.Host,
					ExistingScenario:  existing.Scenario,
					ExistingStartedAt: existing.StartedAt,
					SUTURL:            l.sutURL,
				}
			}
			return ErrConcurrentRun
		} else if !errors.Is(err, mongo.ErrNoDocuments) {
			return fmt.Errorf("query runlock: %w", err)
		}
		// No active run; proceed to insert.
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
	l.runID = runID
	return nil
}
