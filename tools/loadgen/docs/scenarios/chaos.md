# Chaos overlay (Phase 3 §3.17)

## Tool

[toxiproxy](https://github.com/shopify/toxiproxy) — TCP fault-injection
proxy. Richer fault matrix than pumba (latency / bandwidth / slow_close /
timeout / slicer / limit_data) and a viable K8s reuse story.

## Architecture

`docker-compose.chaos.yml` adds two services under `profiles: [chaos]`:

- `toxiproxy`: the admin server + proxies on ports 22220 (NATS) / 22221
  (Cassandra). Operators wire clients to `nats://toxiproxy:22220` (etc.)
  when running with `--profile chaos`.
- `toxiproxy-init`: one-shot init that creates the two proxies via
  `toxiproxy-cli create`.

## `loadgen chaos` admin subcommand

Built into the loadgen binary. Calls the toxiproxy HTTP API directly.

```
# List installed toxics on the NATS proxy
loadgen chaos list nats_inbound

# Add a 200ms latency with 50ms jitter
loadgen chaos add nats_inbound my-latency latency latency=200 jitter=50

# Add a 1 KB/s bandwidth limit
loadgen chaos add nats_inbound throttle bandwidth rate=1

# Remove a toxic
loadgen chaos remove nats_inbound my-latency
```

The TOXIPROXY_URL env var (default `http://localhost:8474`) selects the
toxiproxy admin endpoint.

## Toxic types

| Type        | Attributes                                      | Effect                             |
|-------------|-------------------------------------------------|------------------------------------|
| latency     | latency, jitter (ms)                            | Adds delay to packets              |
| bandwidth   | rate (KB/s)                                     | Limits throughput                  |
| slow_close  | delay (ms)                                      | Delays TCP close                   |
| timeout     | timeout (ms)                                    | Closes the connection after timeout|
| slicer      | average_size, size_variation, delay             | Splits TCP packets                 |
| limit_data  | bytes                                           | Closes the connection after N bytes|

## `scripts/run-chaos.sh`

End-to-end driver: brings up the overlay, runs a 5m baseline messaging-
pipeline, applies latency + bandwidth toxics mid-run, lets the operator
observe verdict + Grafana degradation behavior.

```
DURATION=5m PRESET=realistic RATE=200 ./scripts/run-chaos.sh
```

## SUT graceful-degradation expectations

- Latency 200ms / jitter 50ms on NATS: expect omission p99 to spike,
  possibly DEGRADED verdict. RAW history p99 may also rise.
- Bandwidth 1 KB/s on NATS: published_queued grows faster than acked;
  LoadgenAsyncAckBacklog alert should fire within 5 min.
- These are *observations*, not strict assertions. The point is to
  characterize how the SUT behaves, not to gate releases on specific
  values.
