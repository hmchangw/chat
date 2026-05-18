// tools/loadgen/scenario_mutate.go
//
// message-mutate scenario (Phase 3 §3.6): emits edits/deletes on canonical
// subjects (chat.msg.canonical.{siteID}.edited / .deleted) for recently-
// published messages. Co-runs with messaging-pipeline (or any scenario that
// populates Collector.RecentMessages). Supports an edit-age distribution that
// models realistic edit patterns (most edits are "typo fixes" within seconds;
// some are "correction edits" hours later).
package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"
)

// messageMutateScenario emits edits/deletes on canonical subjects for recently-
// published messages. Co-runs with messaging-pipeline (or any scenario that
// populates Collector.RecentMessages). Supports an edit-age distribution that
// models realistic edit patterns (most edits are "typo fixes" within seconds;
// some are "correction edits" hours later).
type messageMutateScenario struct{}

func (messageMutateScenario) Name() string          { return "message-mutate" }
func (messageMutateScenario) DefaultPreset() string { return "realistic" }

// NewGenerator constructs the message-mutate load generator.
func (messageMutateScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &messageMutateGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(messageMutateScenario{}) }

// messageMutateGenerator is the runner produced by messageMutateScenario.
type messageMutateGenerator struct {
	deps ScenarioDeps
	rf   *runFlags
}

// pickMutationKind returns "edit" or "delete" weighted by Preset.EditRate
// vs DeleteRate. If both are zero, defaults to "edit" (treat as edit-only).
func pickMutationKind(p *Preset, rng *rand.Rand) string {
	total := p.EditRate + p.DeleteRate
	if total <= 0 {
		return "edit"
	}
	if rng.Float64() < p.EditRate/total {
		return "edit"
	}
	return "delete"
}

// pickEditTargetByAge picks a message ID weighted by the edit-age distribution.
// Returns (messageID, ringName) where ringName is "typo" or "correction".
// Falls back to the non-empty ring when the preferred ring is empty.
func pickEditTargetByAge(typoRing, correctionRing []string, typoFraction float64, rng *rand.Rand) (string, string) {
	if rng.Float64() < typoFraction {
		if len(typoRing) > 0 {
			return typoRing[rng.IntN(len(typoRing))], "typo"
		}
	}
	if len(correctionRing) > 0 {
		return correctionRing[rng.IntN(len(correctionRing))], "correction"
	}
	if len(typoRing) > 0 {
		return typoRing[rng.IntN(len(typoRing))], "typo"
	}
	return "", "" // no candidates
}

// parseEditAgeDistribution parses "0.7,0.3" into the typo fraction.
// Validates that the two fractions sum to ~1.0 within tolerance.
func parseEditAgeDistribution(s string) (float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, errors.New("expected two comma-separated fractions (typo,correction)")
	}
	typo, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, fmt.Errorf("parse typo fraction: %w", err)
	}
	correction, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, fmt.Errorf("parse correction fraction: %w", err)
	}
	sum := typo + correction
	if sum < 0.99 || sum > 1.01 {
		return 0, fmt.Errorf("typo + correction must sum to ~1.0; got %.3f", sum)
	}
	return typo, nil
}

// newDeterministicRand returns a *rand.Rand with a fixed seed (for tests).
func newDeterministicRand(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, 0))
}

// Run is a SKELETON for Phase 3.6. Real wire-up emits actual NATS publishes
// to chat.msg.canonical.{siteID}.edited / .deleted subjects via the
// Publisher. See TODO below.
func (g *messageMutateGenerator) Run(ctx context.Context) error {
	rate := g.rf.MutateRate
	if rate <= 0 {
		rate = 5 // default 5 mutations/sec
	}
	typoFrac := 0.7
	if g.rf.EditAgeDistribution != "" {
		if t, err := parseEditAgeDistribution(g.rf.EditAgeDistribution); err == nil {
			typoFrac = t
		}
	}
	_ = typoFrac // wired below

	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))
	var wg sync.WaitGroup
	defer wg.Wait()

	// Reuses the request-latency cell with kind="edit"|"delete" — Phase 1a.2's
	// Collector.RecordRequest API.

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			recents := g.deps.Collector().RecentMessages(200)
			if len(recents) == 0 {
				continue
			}
			kind := pickMutationKind(g.deps.Preset(), rng)
			target := recents[rng.IntN(len(recents))]

			wg.Add(1)
			go func(msgID string, k string) {
				defer wg.Done()
				start := time.Now()
				err := g.emitMutation(ctx, k, msgID)
				lat := time.Since(start)
				phase := "measured"
				if time.Now().Before(g.deps.WarmupDeadline()) {
					phase = "warmup"
				}
				g.deps.Collector().RecordRequest("message-mutate", k, phase, lat, err != nil)
			}(target.MessageID, kind)
		}
	}
}

// emitMutation is a STUB for Phase 3.6 initial landing. TODO follow-up:
// publish a real canonical-edit or canonical-delete event via g.deps.Publisher
// on chat.msg.canonical.{siteID}.edited / .deleted.
func (g *messageMutateGenerator) emitMutation(_ context.Context, _ string, _ string) error {
	return nil // PLACEHOLDER
}
