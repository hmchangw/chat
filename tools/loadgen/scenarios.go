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

// scenarioDescriptions provides one-line operator-facing descriptions for
// the built-in scenarios. Scenarios that implement ScenarioDescription
// override this table.
var scenarioDescriptions = map[string]string{
	"messaging-pipeline":   "default broad-spectrum write load via gatekeeper",
	"history-read":         "history-service read benchmark; needs pre-seeded msgs",
	"search-read":          "search-service query benchmark",
	"room-rpc":             "room-service request/reply benchmark",
	"raw-consistency":      "publish→read-path RAW lag (skeleton: history poll only)",
	"large-room-broadcast": "10k-member fanout; preset shapes: announce/firehose/bot (skeleton — no real subs)",
	"notification-fanout":  "in-app notification publish→subscriber lag",
	"message-mutate":       "edit/delete on recently-published messages (skeleton — no real edit/delete publishes yet)",
	"subscription-churn":   "join/leave churn on dedicated fixture pool",
	"auth-load":            "auth-service POST /auth HTTP benchmark + reconnect-storm",
	"federation-lag":       "2-site OUTBOX→INBOX lag across 4 stages (skeleton)",
	"first-dm":             "lazy DM-room create path with 4-stage lag (skeleton)",
	"room-open":            "composite: 4-5 legs fired in parallel via errgroup",
	"read-receipts":        "MessageRead emission for a fraction of recipients",
	"search-sync-lag":      "publish→search.messages visibility lag; requires --inject=canonical; bootstraps user-room ACL",
}

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
		if desc == "" {
			desc = scenarioDescriptions[sc.Name()]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", sc.Name(), sc.DefaultPreset(), desc)
	}
	tw.Flush()
	return 0
}
