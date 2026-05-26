// Command runner executes the v2 integration test suite.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	rt "github.com/hmchangw/chat/tools/integration-suite-v2/internal/runtime"
)

func main() {
	cfg := &rt.Config{
		AuthURL:            env("AUTH_SERVICE_URL", "http://localhost:8080"),
		NATSURL:            env("NATS_URL", "nats://localhost:4222"),
		NATSCredsFile:      env("NATS_CREDS_FILE", ""),
		MongoURI:           env("MONGO_URI", "mongodb://localhost:27017"),
		MongoDB:            env("MONGO_DB", "chat"),
		ValkeyAddr:         env("VALKEY_ADDR", ""),
		SiteID:             env("SITE_ID", "site-local"),
		ScenariosDir:       env("SCENARIOS_DIR", "scenarios"),
		CatalogsDir:        env("CATALOGS_DIR", "catalogs"),
		OutputPath:         env("OUTPUT_PATH", "last-run.md"),
		ApprovedOutputPath: env("APPROVED_OUTPUT_PATH", ""),
		PerformancePath:    env("PERFORMANCE_PATH", ""),
		RepoRoot:           env("REPO_ROOT", ""),
	}

	report, err := rt.Run(context.Background(), cfg)
	if err != nil {
		slog.Error("run failed", "err", err)
		os.Exit(2)
	}

	fmt.Printf("Run %s — %d cases\n", report.RunID, len(report.Cases))
	pass, fail := 0, 0
	for i := range report.Cases {
		if report.Cases[i].Verdict.Outcome == "pass" {
			pass++
		} else {
			fail++
		}
	}
	fmt.Printf("pass: %d  fail: %d\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
