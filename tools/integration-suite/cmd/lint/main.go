// Command lint verifies that every @blindspot:<slug> in a feature
// file has a matching entry in blindspots.md, and vice versa.
package main

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	is "github.com/hmchangw/chat/tools/integration-suite"
)

func main() {
	featuresRoot := "tools/integration-suite/features"
	registerPath := "docs/integration-suite/blindspots.md"

	var inFeatures []string
	err := filepath.WalkDir(featuresRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".feature") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		inFeatures = append(inFeatures, is.ExtractBlindspotSlugs(string(body))...)
		return nil
	})
	if err != nil {
		slog.Error("lint: walk features", "err", err)
		os.Exit(2)
	}

	regBody, err := os.ReadFile(registerPath)
	if err != nil {
		// blindspots.md doesn't exist yet (Task 22 creates it). If there
		// are also no blindspots used in features, it's OK.
		if len(inFeatures) == 0 {
			fmt.Println("integration-suite-lint: ok — no blindspots in features, no register required yet")
			return
		}
		slog.Error("lint: read register", "err", err, "path", registerPath)
		os.Exit(2)
	}
	inRegister := is.ExtractRegisteredSlugs(string(regBody))

	missingInRegister, missingInFeatures := is.DiffSlugs(inFeatures, inRegister)

	if len(missingInRegister) == 0 && len(missingInFeatures) == 0 {
		fmt.Println("integration-suite-lint: ok — blindspot slugs are consistent")
		return
	}

	if len(missingInRegister) > 0 {
		fmt.Printf("\nBlindspots used in features but missing from %s:\n%s\n",
			registerPath, is.JoinLines(missingInRegister))
	}
	if len(missingInFeatures) > 0 {
		fmt.Printf("\nBlindspots registered in %s but no longer referenced by any feature:\n%s\n",
			registerPath, is.JoinLines(missingInFeatures))
	}
	os.Exit(1)
}
