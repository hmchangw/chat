package readers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ContainerLogsReader tails `docker logs -f <container>` from the
// start of the scenario and emits one Event per JSON-parseable line.
// Event.Traceparent is populated from the line's `traceparent` field
// when present, empty otherwise. The verdict classifier handles
// trace presence/absence at verdict time — this reader does NOT
// filter by trace.
type ContainerLogsReader struct {
	Container string // docker container name
	Service   string // service name (for OwnerSvc)
	Location  string // catalog location (e.g., "logs.room-service")
}

// NewContainerLogsReader returns a ContainerLogsReader for the named container.
func NewContainerLogsReader(container, service, location string) *ContainerLogsReader {
	return &ContainerLogsReader{Container: container, Service: service, Location: location}
}

// Watch starts `docker logs -f` on the container and emits one Event per
// structured JSON log line whose timestamp is >= start. `traceparent` is
// accepted to satisfy the Reader interface but not used — slog lines
// carry their own `traceparent` field when production wires OTel into
// the logger, which is the source-of-truth this reader extracts.
//
// Container resolution: the literal `r.Container` name (set at
// registration) is the FALLBACK. The primary path queries
// `docker ps --filter label=com.docker.compose.service=<r.Service>`
// so the reader works regardless of docker-compose's project-name
// prefix (e.g. `chat-local-services-room-worker-1` vs literal
// `room-worker`). §9.8 of the corrections spec.
func (r *ContainerLogsReader) Watch(ctx context.Context, _ string, start time.Time) (<-chan Event, error) {
	containerRef := r.resolveContainerRef(ctx)
	out := make(chan Event, 64)
	cmd := exec.CommandContext(ctx, "docker", "logs", "-f", "--since", start.Format(time.RFC3339), containerRef)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("logs reader: pipe %s: %w", containerRef, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("logs reader: start %s: %w", containerRef, err)
	}

	go func() {
		defer close(out)
		defer cmd.Wait() //nolint:errcheck
		scanner := bufio.NewScanner(stdout)
		// Allow lines up to 1 MiB
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				// Not JSON; skip
				continue
			}
			ts := parseLogTime(entry)
			if ts.Before(start) {
				continue
			}
			tp, _ := entry["traceparent"].(string)
			out <- Event{
				Location:    r.Location,
				Timestamp:   ts,
				Traceparent: tp,
				OwnerSvc:    r.Service,
				Payload:     entry,
			}
		}
	}()
	return out, nil
}

// resolveContainerRef returns a docker-CLI-usable reference (ID or
// name) for the container hosting this reader's service. Strategy:
//  1. Query `docker ps --filter label=com.docker.compose.service=<svc>`
//     for the first match — the compose service label is set by docker
//     compose regardless of the project's name-prefixing scheme.
//  2. Fall back to the literal `r.Container` name if step 1 returns
//     nothing (e.g. running under plain `docker run` without compose).
//
// Errors from docker are also fallback triggers so this never blocks
// the caller — the failure mode is "no events emitted" via the later
// docker logs subprocess, which is the same as the pre-§9.8 behavior.
func (r *ContainerLogsReader) resolveContainerRef(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "docker", "ps",
		"--filter", fmt.Sprintf("label=com.docker.compose.service=%s", r.Service),
		"--format", "{{.ID}}")
	out, err := cmd.Output()
	if err != nil {
		return r.Container
	}
	first := strings.TrimSpace(string(out))
	if first == "" {
		return r.Container
	}
	// Multiple matches → take the first line.
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = strings.TrimSpace(first[:i])
	}
	if first == "" {
		return r.Container
	}
	return first
}

// parseLogTime extracts the timestamp from a slog JSON log entry.
// The slog JSON handler emits a "time" field in RFC3339Nano.
func parseLogTime(entry map[string]any) time.Time {
	if s, ok := entry["time"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Now()
}
