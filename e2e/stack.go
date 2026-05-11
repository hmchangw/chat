//go:build e2e

package e2e

import "github.com/hmchangw/chat/e2e/harness"

// stack is the package-level singleton populated by TestMain (main_test.go).
// Exposed via Stack() so files in e2e/scenarios/ can import it -- _test.go
// files don't export symbols to other packages.
var stack *harness.Stack

// Stack returns the running stack handle. Tests reach it for per-site
// endpoints, e.g. e2e.Stack().SiteA.Authenticate(t, ctx, "alice").
//
// Returns nil if called outside of an `e2e` build (build tag gated) or
// before TestMain has run -- the smoke test asserts non-nil up front.
func Stack() *harness.Stack { return stack }
