// Command steps prints all currently registered godog step regexes,
// grouped by source file. It does so by running `go test` against
// the integration suite in dry-run mode and parsing the output.
package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	cmd := exec.Command("go", "test",
		"-run", "TestFeatures",
		"-v",
		"./tools/integration-suite/...",
	)
	cmd.Env = append(os.Environ(),
		"GODOG_DRY_RUN=1",
		"SITES=tw",
		"PRIMARY_SITE=tw",
		"AUTH_SERVICE_URL_TW=http://example.invalid",
		"ROOM_SERVICE_URL_TW=http://example.invalid",
		"NATS_URL_TW=nats://example.invalid:4222",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "steps:", err)
		os.Exit(1)
	}
}
