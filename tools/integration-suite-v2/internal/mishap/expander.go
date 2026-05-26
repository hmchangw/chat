package mishap

// PowerSet returns every subset of items (including the empty set).
// Order within each subset matches the input order.
func PowerSet(items []string) [][]string {
	n := len(items)
	total := 1 << n
	out := make([][]string, 0, total)
	for mask := 0; mask < total; mask++ {
		var subset []string
		for i := 0; i < n; i++ {
			if mask&(1<<i) != 0 {
				subset = append(subset, items[i])
			}
		}
		out = append(out, subset)
	}
	return out
}

// Expand computes the power set of applicable mishaps minus the ignore list.
// applicable = the names of every mishap in the catalog.
// ignore     = the names listed in the scenario's mishaps.ignore (blacklist).
// Returns every subset (including the empty subset = the "happy" case).
func Expand(applicable, ignore []string) [][]string {
	ignored := map[string]bool{}
	for _, i := range ignore {
		ignored[i] = true
	}
	var filtered []string
	for _, a := range applicable {
		if !ignored[a] {
			filtered = append(filtered, a)
		}
	}
	return PowerSet(filtered)
}
