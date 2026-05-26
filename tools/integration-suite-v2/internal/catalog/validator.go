package catalog

import "fmt"

// Validate checks that every executor name referenced from the catalog
// YAML resolves to a Go symbol (knownExecutors), and every service owner
// listed on a reader is a real service (knownServices).
//
// Returns a list of error strings; empty list = catalog is valid.
func Validate(c *Catalog, knownExecutors, knownServices map[string]bool) []string {
	var errs []string
	for _, v := range c.Verbs {
		if !knownExecutors[v.Executor] {
			errs = append(errs, fmt.Sprintf("verb %q: executor %q not registered", v.Name, v.Executor))
		}
	}
	for _, r := range c.Readers {
		if !knownExecutors[r.Executor] {
			errs = append(errs, fmt.Sprintf("reader %q: executor %q not registered", r.Location, r.Executor))
		}
		for _, o := range r.Owners {
			if !knownServices[o] {
				errs = append(errs, fmt.Sprintf("reader %q: owner %q is not a registered service", r.Location, o))
			}
		}
	}
	return errs
}
