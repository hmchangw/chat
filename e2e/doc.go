//go:build e2e

// Package e2e is the project's end-to-end test suite.
//
// The suite drives the full multi-site backend through the same NATS
// subjects and HTTP routes a real client would use. It boots a multi-
// container two-site stack (full dep set per site, federation via NATS
// gateways) via testcontainers-go's compose module against
// docker-local/e2e/compose.e2e.yaml.
//
// Build tag e2e gates the entire package -- `go test ./...` without the
// tag ignores it. Run with: make e2e (full lifecycle) or make e2e-only
// (assumes a stack already running via make e2e-up).
//
// See docs/superpowers/plans/2026-05-08-e2e-tests.md plus the companion
// amendments doc 2026-05-08-e2e-tests-amendments.md for the plan that
// produced this code.
package e2e
