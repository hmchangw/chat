//go:build integration

package testutil

// TerminateAll stops every process-shared container started by this
// package. Each TerminateXxx is a no-op if its container was never
// started, so this is safe from any service's TestMain. Use via
// testutil.RunTests for the standard wrap.
//
// StartValkeyCluster (per-test) is unaffected — those containers are
// cleaned up by their own t.Cleanup hooks. TerminateValkey only stops
// the shared cluster from SharedValkeyCluster, if one was started.
func TerminateAll() {
	TerminateMongo()
	TerminateCassandra()
	TerminateMinIO()
	TerminateElasticsearch()
	TerminateNATS()
	TerminateValkey()
}
