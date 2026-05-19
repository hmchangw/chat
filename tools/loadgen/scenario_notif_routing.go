// tools/loadgen/scenario_notif_routing.go
//
//go:build notif_routing_ready

// Package main: per-channel (push/email) notification routing scenario.
//
// PHASE 3.4b GATING NOTE:
// This file compiles only with build tag `notif_routing_ready` because
// pkg/subject does not yet define per-channel notification routing builders.
// When the SUT adds:
//   - subject.PushNotification(account string) string
//   - subject.EmailNotification(account string) string
//
// remove the build tag and wire the OfflineUserFraction / DNDUserFraction
// preset fields together with the per-channel metric labels.
//
// See docs/scenarios/notification-fanout.md for the activation playbook.
//
// Phase 3 §3.4b (loadgen v2 plan).
package main

import (
	"context"
)

// notifRoutingScenario is the Phase 3.4b per-channel (push/email) notification
// routing scenario. It is gated behind the `notif_routing_ready` build tag
// until pkg/subject exposes PushNotification and EmailNotification builders.
type notifRoutingScenario struct{}

func (notifRoutingScenario) Name() string          { return "notif-routing" }
func (notifRoutingScenario) DefaultPreset() string { return "realistic" }

func (notifRoutingScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &notifRoutingGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(notifRoutingScenario{}) }

type notifRoutingGenerator struct {
	deps ScenarioDeps
	rf   *runFlags
}

// Run is a SKELETON. Once push/email notification subjects land in pkg/subject:
//  1. Mark round(len(users)*preset.OfflineUserFraction) users as offline
//     (exclude from in-app subscriber set).
//  2. Mark round(len(users)*preset.DNDUserFraction) users as DND
//     (suppress push/email delivery expectation).
//  3. For each offline non-DND user, subscribe to
//     subject.PushNotification(user.Account) via g.deps.Subscribers() and
//     record receipt lag with channel="push" into loadgen_notification_lag_seconds.
//  4. Optionally subscribe to subject.EmailNotification(user.Account) and
//     record with channel="email".
//  5. See docs/scenarios/notification-fanout.md §3.4b for the full algorithm.
func (g *notifRoutingGenerator) Run(ctx context.Context) error {
	// TODO(phase-3.4b): wire push/email routing once pkg/subject exposes the
	// required builders. See file header for the full activation checklist.
	<-ctx.Done()
	return nil
}
