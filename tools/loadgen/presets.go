// tools/loadgen/presets.go
//
// `loadgen presets` subcommand — lists all built-in presets with their
// shape and a short description (cross-phase Task X.1).
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
)

// runPresets implements the `loadgen presets` subcommand.
func runPresets(_ *config, args []string) int {
	return runPresetsTo(os.Stdout, args)
}

func runPresetsTo(w io.Writer, _ []string) int {
	presets := AllBuiltinPresets()
	sort.Slice(presets, func(i, j int) bool { return presets[i].Name < presets[j].Name })

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PRESET\tUSERS\tROOMS\tDM%\tDESCRIPTION")
	fmt.Fprintln(tw, "------\t-----\t-----\t---\t-----------")
	for i := range presets {
		p := &presets[i]
		dmPct := int(p.DMRatio * 100)
		desc := describePreset(p.Name)
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d%%\t%s\n", p.Name, p.Users, p.Rooms, dmPct, desc)
	}
	tw.Flush()
	return 0
}

// describePreset returns a short human-readable description for a built-in preset.
func describePreset(name string) string {
	descriptions := map[string]string{
		"messaging-pipeline":   "default broad-spectrum write load",
		"realistic":            "balanced traffic shape (60% DM, 40% channel)",
		"channel-heavy":        "team-channel-dominated workplace shape",
		"dm-heavy":             "DM-dominated consumer shape",
		"history-read":         "history-service read benchmark",
		"search-read":          "search-service query benchmark",
		"small":                "minimal smoke-test footprint",
		"medium":               "typical CI-friendly footprint",
		"large":                "heavy load shape",
		"announce-room":        "10k members, 1 broadcast/min (large-room scenario)",
		"firehose-room":        "1k members, 50 writes/sec (large-room scenario)",
		"bot-room":             "100 members, 200 writes/sec from 3 bots (large-room)",
		"incident-burst":       "5 rps baseline with 200 rps bursts every 30s",
		"auth-reconnect-storm": "1k idle conns dropped at T+30s (auth-load)",
	}
	return descriptions[name]
}
