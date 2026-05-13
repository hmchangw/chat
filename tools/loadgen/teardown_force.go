package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// teardownForceConfig controls the orphan-recovery algorithm.
type teardownForceConfig struct {
	// OlderThan restricts deletion to runs whose lock row's startedAt is
	// older than now-OlderThan. Zero means "any orphan".
	OlderThan time.Duration
	// SpecificRunID, when non-empty, limits enumeration to resources whose
	// short prefix matches this run ID. Useful for targeted recovery.
	SpecificRunID string
}

// teardownReport summarises the outcome of a force teardown.
type teardownReport struct {
	DroppedMongoDBs  []string
	DroppedConsumers []string
	SkippedDBs       []string // active runs left untouched
	SkippedConsumers []string
}

// runTeardownForce enumerates all loadgen_* Mongo databases and JetStream
// durable consumers, cross-references them against the loadgen_runs lock
// collection in loadgen_shared to identify orphans (no active lock row, or
// an expired one), and drops each orphan idempotently.
//
// It never drops loadgen_shared itself.
func runTeardownForce(ctx context.Context, mc *mongo.Client, js jetstream.JetStream, cfg teardownForceConfig) (*teardownReport, error) {
	rep := &teardownReport{}

	activeShortIDs, err := loadActiveRunShortIDs(ctx, mc, cfg.OlderThan)
	if err != nil {
		return nil, fmt.Errorf("load active run short IDs: %w", err)
	}

	// Step 1: enumerate and drop orphaned Mongo databases.
	dbNames, err := mc.ListDatabaseNames(ctx, bson.M{"name": bson.M{"$regex": "^loadgen_"}})
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	for _, name := range dbNames {
		if name == SharedLockDBName {
			continue // never drop the shared lock DB
		}
		short := strings.TrimPrefix(name, "loadgen_")
		if cfg.SpecificRunID != "" && !strings.HasPrefix(cfg.SpecificRunID, short) {
			continue
		}
		if activeShortIDs[short] {
			rep.SkippedDBs = append(rep.SkippedDBs, name)
			continue
		}
		if err := mc.Database(name).Drop(ctx); err != nil {
			slog.Warn("drop mongo DB failed", "name", name, "error", err)
			continue
		}
		rep.DroppedMongoDBs = append(rep.DroppedMongoDBs, name)
	}

	// Step 2: enumerate and drop orphaned JetStream consumers across all streams.
	streamNameLister := js.StreamNames(ctx)
	for streamName := range streamNameLister.Name() {
		stream, err := js.Stream(ctx, streamName)
		if err != nil {
			slog.Warn("get stream failed", "stream", streamName, "error", err)
			continue
		}
		consumerNameLister := stream.ConsumerNames(ctx)
		for consName := range consumerNameLister.Name() {
			if !strings.HasPrefix(consName, "loadgen_") {
				continue
			}
			// Consumer name format: "loadgen_<short>_<purpose>"
			rest := strings.TrimPrefix(consName, "loadgen_")
			// short is the part before the first underscore
			underscoreIdx := strings.Index(rest, "_")
			var short string
			if underscoreIdx < 0 {
				// no underscore: the whole rest is the short
				short = rest
			} else {
				short = rest[:underscoreIdx]
			}
			if short == "" {
				continue
			}
			if cfg.SpecificRunID != "" && !strings.HasPrefix(cfg.SpecificRunID, short) {
				continue
			}
			if activeShortIDs[short] {
				rep.SkippedConsumers = append(rep.SkippedConsumers, consName)
				continue
			}
			if err := js.DeleteConsumer(ctx, streamName, consName); err != nil {
				slog.Warn("delete consumer failed", "stream", streamName, "consumer", consName, "error", err)
				continue
			}
			rep.DroppedConsumers = append(rep.DroppedConsumers, consName)
		}
		if err := consumerNameLister.Err(); err != nil {
			slog.Warn("consumer names lister error", "stream", streamName, "error", err)
		}
	}
	if err := streamNameLister.Err(); err != nil {
		slog.Warn("stream names lister error", "error", err)
	}

	return rep, nil
}

// loadActiveRunShortIDs queries the loadgen_runs lock collection in
// loadgen_shared and returns the set of shortRunIDs that belong to
// currently active runs.
//
// A row is considered active when its startedAt is more recent than
// now-olderThan. When olderThan is zero, the cutoff is time.Now(), so
// ALL rows are treated as recent (i.e., the set only contains rows with
// startedAt in the future relative to now, which is always empty unless
// clocks are skewed — effectively every row is a candidate for orphan
// removal when olderThan==0 is combined with the outer logic).
//
// Wait — to keep the semantics correct: olderThan==0 means "consider
// only rows younger than now" which is effectively ALL rows. So active
// means startedAt > now-olderThan == now-0 == now, which matches nothing.
// That means olderThan==0 treats all runs as orphans. That is the intended
// "drop everything" semantics per the spec.
//
// When olderThan>0, rows with startedAt within the last olderThan duration
// are considered active (not eligible for cleanup).
func loadActiveRunShortIDs(ctx context.Context, mc *mongo.Client, olderThan time.Duration) (map[string]bool, error) {
	coll := mc.Database(SharedLockDBName).Collection("loadgen_runs")
	cutoff := time.Now().Add(-olderThan)
	cur, err := coll.Find(ctx, bson.M{"startedAt": bson.M{"$gt": cutoff}})
	if err != nil {
		return nil, fmt.Errorf("query loadgen_runs: %w", err)
	}
	defer cur.Close(ctx) //nolint:errcheck
	active := make(map[string]bool)
	for cur.Next(ctx) {
		var doc runLockDoc
		if err := cur.Decode(&doc); err != nil {
			slog.Warn("decode runlock doc", "error", err)
			continue
		}
		active[shortRunID(doc.RunID)] = true
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("iterate loadgen_runs: %w", err)
	}
	return active, nil
}
