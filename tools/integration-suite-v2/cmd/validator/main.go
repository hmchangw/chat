// Command validator loads the catalogs/ directory and the scenarios/drafts/
// directory (both relative to the current working directory, or paths
// passed as positional arguments) and reports any reference drift or
// per-file scenario parse errors. Exits 0 on success, 1 on validation
// errors.
package main

import (
	"fmt"
	"os"

	cat "github.com/hmchangw/chat/tools/integration-suite-v2/internal/catalog"
	sc "github.com/hmchangw/chat/tools/integration-suite-v2/internal/scenario"
)

func main() {
	catRoot := "catalogs"
	scRoot := "scenarios/drafts"
	if len(os.Args) > 1 {
		catRoot = os.Args[1]
	}
	if len(os.Args) > 2 {
		scRoot = os.Args[2]
	}

	c, err := cat.Load(catRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		os.Exit(2)
	}

	knownExecutors := map[string]bool{
		"NATSRequestExecutor":               true,
		"JetStreamPublishExecutor":          true,
		"MongoRoomsReader":                  true,
		"NATSReplyReader":                   true,
		"ContainerLogsReader[room-service]": true,
		"ContainerLogsReader[room-worker]":  true,
		"JetStreamSubjectReader":            true,
	}
	knownServices := map[string]bool{
		"room-service": true,
		"room-worker":  true,
	}

	catErrs := cat.Validate(c, knownExecutors, knownServices)
	loaded, scErrs := sc.LoadAllInDir(scRoot)

	if len(catErrs) > 0 || len(scErrs) > 0 {
		for _, e := range catErrs {
			fmt.Fprintln(os.Stderr, "ERROR:", e)
		}
		for _, e := range scErrs {
			fmt.Fprintln(os.Stderr, "ERROR:", e)
		}
		os.Exit(1)
	}
	fmt.Println("catalog: ok —", len(c.Verbs), "verbs,", len(c.Readers), "readers,", len(c.Services), "services")
	fmt.Println("scenarios: ok —", loaded, "drafts parsed")
}
