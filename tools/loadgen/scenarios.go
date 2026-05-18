// tools/loadgen/scenarios.go
//
// `loadgen scenarios` subcommand — lists all registered scenarios with
// their default preset and optional description (cross-phase Task X.1).
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
)

// ScenarioDescription is an optional extension to the Scenario interface.
// Scenarios that implement it can provide richer info for `loadgen scenarios`.
type ScenarioDescription interface {
	// Describe returns a short (≤80 chars) human description and the list of
	// SUT dependencies the scenario consumes (e.g., "history-service", "mongo").
	Describe() (description string, deps []string)
}

// runScenarios implements the `loadgen scenarios` subcommand.
func runScenarios(_ *config, args []string) int {
	return runScenariosTo(os.Stdout, args)
}

func runScenariosTo(w io.Writer, _ []string) int {
	all := AllScenarios()
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCENARIO\tDEFAULT PRESET\tDESCRIPTION")
	fmt.Fprintln(tw, "--------\t--------------\t-----------")
	for _, name := range names {
		sc := all[name]
		desc := ""
		if d, ok := sc.(ScenarioDescription); ok {
			description, _ := d.Describe()
			desc = description
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", sc.Name(), sc.DefaultPreset(), desc)
	}
	tw.Flush()
	return 0
}
