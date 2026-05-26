// Package harness holds the integration-suite framework internals:
// the per-scenario World, the error classifier, NATS/HTTP transport
// primitives, W3C tracing, the cucumber-JSON parser, the two-score
// reporter, config loading, and the lint/audit logic.
//
// It is internal/ on purpose: scenario authors interact with feature
// files and step definitions in the parent directory, not this package.
// The top-level runner and step _test.go files dot-import it.
package harness
