//go:build integration

package testutil

// TerminateAll stops every process-shared container started by this
// package. Each TerminateXxx is a no-op if its container was never
// started, so this is safe from any service's TestMain. Use via
// testutil.RunTests for the standard wrap.
func TerminateAll() {
	TerminateMongo()
	TerminateCassandra()
	TerminateMinIO()
	TerminateElasticsearch()
	TerminateNATS()
	TerminateValkey()
}
