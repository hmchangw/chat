// Package integrationsuite is the godog runner for the chat backend
// black-box integration suite. This top-level directory is the
// author-facing surface:
//
//	features/        Gherkin scenarios (what authors write)
//	*_steps_test.go  step definitions (the Given/When/Then glue)
//	main_test.go     the godog entrypoint (TestFeatures)
//	README.md        AUTHORING.md   how to run and author
//	cmd/             standalone CLIs (steps, lint, audit)
//
// The framework internals (World, classifier, NATS/HTTP transport,
// tracing, reporter, config, audit, lint) live in internal/harness and
// are intentionally not part of the author-facing surface. Step files
// dot-import internal/harness so scenarios and the AUTHORING.md skeleton
// stay unqualified.
//
// See README.md and AUTHORING.md.
package integrationsuite
