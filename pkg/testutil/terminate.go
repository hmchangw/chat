//go:build integration

package testutil

// TerminateAll stops every process-shared container started by this
// package, in dependency-free order. Each individual Terminate is a
// no-op if its container was never started, so it's safe to call from
// any service's TestMain regardless of which helpers that service uses.
//
// Intended usage:
//
//	func TestMain(m *testing.M) {
//	    code := m.Run()
//	    testutil.TerminateAll()
//	    os.Exit(code)
//	}
//
// Required when running with TESTCONTAINERS_RYUK_DISABLED=true (e.g.
// our CI integration job) — Ryuk would otherwise reap these on process
// exit. Locally Ryuk catches SIGKILL / Ctrl+C, where m.Run never returns.
func TerminateAll() {
	TerminateMongo()
	TerminateCassandra()
	TerminateMinIO()
	TerminateElasticsearch()
	TerminateNATS()
	TerminateValkey()
}
