// scenario_demo.go is a Phase 2 exit demonstration: it proves that adding
// a new scenario requires only ONE new file (no changes to run.go, main.go,
// or readiness.go). The demo scenario panics if invoked — it's never meant
// to actually run, just to register itself.
//
// This file is deleted in the Phase 2 exit cleanup commit. The git history
// preserves the proof.
package main

// demoScenario proves the registry mechanism. Calling NewGenerator panics
// because the demo is never invoked — its purpose is registration alone.
type demoScenario struct{}

func (demoScenario) Name() string          { return "demo-phase2-exit" }
func (demoScenario) DefaultPreset() string { return "small" } // any valid preset

func (demoScenario) NewGenerator(_ ScenarioDeps, _ *runFlags) (Runner, error) {
	panic("demoScenario.NewGenerator must not be invoked — registration proof only")
}

func init() { RegisterScenario(demoScenario{}) }
