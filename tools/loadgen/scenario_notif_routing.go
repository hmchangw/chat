// tools/loadgen/scenario_notif_routing.go
//
//go:build notif_routing_ready

// Phase 3.4b skeleton: per-channel (push/email) notification routing.
//
// PHASE 3.4 GATING NOTE:
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

var _ context.Context // suppress unused-import warning
