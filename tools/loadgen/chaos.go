package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-resty/resty/v2"
)

// newChaosClient builds a Resty client tuned for the toxiproxy admin API:
// short request timeout (chaos ops are local control-plane RPCs, not user
// traffic) and no retries (we want operator errors to surface immediately).
// CLAUDE.md §6 requires Resty for outbound HTTP — never net/http directly.
func newChaosClient() *resty.Client {
	return resty.New().
		SetTimeout(10*time.Second).
		SetHeader("Content-Type", "application/json")
}

// chaosUsage writes the chaos subcommand usage to w.
func chaosUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  loadgen chaos add <proxy> <name> <type> [k=v ...]
  loadgen chaos remove <proxy> <name>
  loadgen chaos list <proxy>

Environment:
  TOXIPROXY_URL  toxiproxy admin endpoint (default: http://localhost:8474)

Examples:
  loadgen chaos add nats_inbound my-latency latency latency=200 jitter=50
  loadgen chaos remove nats_inbound my-latency
  loadgen chaos list nats_inbound
`)
}

// chaosToxic is the toxiproxy API representation of an installed fault.
type chaosToxic struct {
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`
	Attributes map[string]interface{} `json:"attributes"`
}

// chaosAddToxic installs a toxic on the given proxy via toxiproxy HTTP API.
// proxy: e.g., "nats_inbound", "cassandra_inbound" (configured in toxiproxy-init).
// name: operator-chosen instance name (e.g., "latency-200ms-burst").
// toxicType: latency | bandwidth | slow_close | timeout | slicer | limit_data.
// attrs: type-specific knobs (e.g., {"latency": 200, "jitter": 50} for latency).
func chaosAddToxic(ctx context.Context, baseURL, proxy, name, toxicType string, attrs interface{}) error {
	resp, err := newChaosClient().R().
		SetContext(ctx).
		SetBody(map[string]interface{}{
			"name":       name,
			"type":       toxicType,
			"attributes": attrs,
		}).
		Post(baseURL + "/proxies/" + proxy + "/toxics")
	if err != nil {
		return fmt.Errorf("chaos add request: %w", err)
	}
	if resp.StatusCode() >= 400 {
		return fmt.Errorf("toxiproxy add toxic: HTTP %d: %s", resp.StatusCode(), resp.String())
	}
	return nil
}

// chaosRemoveToxic removes a named toxic from a proxy. A 404 is treated as
// success so the operator can call remove idempotently after a partial state.
func chaosRemoveToxic(ctx context.Context, baseURL, proxy, name string) error {
	resp, err := newChaosClient().R().
		SetContext(ctx).
		Delete(baseURL + "/proxies/" + proxy + "/toxics/" + name)
	if err != nil {
		return fmt.Errorf("chaos remove request: %w", err)
	}
	if resp.StatusCode() >= 400 && resp.StatusCode() != http.StatusNotFound {
		return fmt.Errorf("toxiproxy remove toxic: HTTP %d: %s", resp.StatusCode(), resp.String())
	}
	return nil
}

// chaosListToxics returns all installed toxics on a proxy.
func chaosListToxics(ctx context.Context, baseURL, proxy string) ([]chaosToxic, error) {
	var toxics []chaosToxic
	resp, err := newChaosClient().R().
		SetContext(ctx).
		SetResult(&toxics).
		Get(baseURL + "/proxies/" + proxy + "/toxics")
	if err != nil {
		return nil, fmt.Errorf("chaos list request: %w", err)
	}
	if resp.StatusCode() >= 400 {
		return nil, fmt.Errorf("toxiproxy list toxics: HTTP %d: %s", resp.StatusCode(), resp.String())
	}
	return toxics, nil
}

// isValidChaosAction validates the chaos subcommand action.
func isValidChaosAction(action string) bool {
	switch action {
	case "add", "remove", "list":
		return true
	}
	return false
}

// runChaos is the entry point for `loadgen chaos <add|remove|list> ...`.
// Subcommand surface (current minimal set):
//
//	loadgen chaos add <proxy> <name> <type> [k=v ...]
//	loadgen chaos remove <proxy> <name>
//	loadgen chaos list <proxy>
//
// TOXIPROXY_URL env var (default http://localhost:8474) selects the
// toxiproxy admin endpoint.
func runChaos(_ context.Context, cfg *config, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: loadgen chaos <add|remove|list> ...")
		return 2
	}
	if args[0] == "--help" || args[0] == "-h" {
		chaosUsage(os.Stdout)
		return 0
	}
	action := args[0]
	if !isValidChaosAction(action) {
		fmt.Fprintf(os.Stderr, "unknown chaos action %q (want: add | remove | list)\n", action)
		chaosUsage(os.Stderr)
		return 2
	}

	baseURL := cfg.ToxiproxyURL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch action {
	case "list":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: loadgen chaos list <proxy>")
			return 2
		}
		toxics, err := chaosListToxics(ctx, baseURL, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		for _, t := range toxics {
			slog.Info("chaos toxic", "name", t.Name, "type", t.Type, "attrs", t.Attributes)
		}
		return 0
	case "remove":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: loadgen chaos remove <proxy> <name>")
			return 2
		}
		if err := chaosRemoveToxic(ctx, baseURL, args[1], args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		slog.Info("chaos toxic removed", "proxy", args[1], "name", args[2])
		return 0
	case "add":
		if len(args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: loadgen chaos add <proxy> <name> <type> [k=v ...]")
			return 2
		}
		proxy, name, toxicType := args[1], args[2], args[3]
		attrs := map[string]int{}
		for _, kv := range args[4:] {
			var k string
			var v int
			if _, err := fmt.Sscanf(kv, "%[^=]=%d", &k, &v); err != nil {
				fmt.Fprintf(os.Stderr, "bad attribute %q: expected k=v with int value\n", kv)
				return 2
			}
			attrs[k] = v
		}
		if err := chaosAddToxic(ctx, baseURL, proxy, name, toxicType, attrs); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		slog.Info("chaos toxic added", "proxy", proxy, "name", name, "type", toxicType, "attrs", attrs)
		return 0
	}
	return 0
}
