# Runbook: loadgen run printed UNTRUSTED

When a loadgen run finishes with `RUN QUALITY: UNTRUSTED`, the printed
numbers cannot be defended. Use this runbook to diagnose what went wrong.

## Quick triage

1. Locate the run's artifact bundle:
   ```
   ls runs/<run_id>/
   ```
2. Read `summary.json` for the verdict + issues list:
   ```bash
   cat runs/<run_id>/summary.json | jq '.verdict, .issues'
   ```
3. Match the first issue to the section below.

## UNTRUSTED triggers

### "async drain timed out"

The harness publishes asynchronously. After the run window ended, it waited
up to `--async-drain-timeout` for all pending JetStream acks to land — and
the timer expired with acks still outstanding.

**Diagnosis**: `loadgen_publish_errors_total{reason="async_ack"}` should be
non-zero. Check JetStream consumer lag via the `dashboards` profile.

**Remediation**:
- Increase `--async-drain-timeout` (default 25s) for slow SUTs.
- Reduce `--rate` to fit the SUT's actual capacity.
- Investigate JetStream backpressure: compare `loadgen_publish_by_room_type_total`
  vs `loadgen_publish_errors_total{reason="async_ack"}` ratio.

### "measured window shorter than abort-p99-sustain"

The measured window must be longer than `--abort-p99-sustain` so the abort
watcher has time to observe enough samples to fire.

**Remediation**: increase `--duration`, or decrease `--abort-p99-sustain`.

### "warmup error rate > 20%"

More than 20% of warmup publishes errored. Either the SUT is broken
during warmup, or the preset is too aggressive for the current SUT.

**Remediation**:
- Run `loadgen doctor` to check host readiness.
- Investigate SUT health via `./tools/loadgen/scripts/triage.sh <run_id>`.
- Try a smaller preset (`--preset=small`) to confirm the SUT works at all.
- Check SUT service logs for startup errors.

### "settle phase incomplete"

The settle phase polled for N messages to become visible and timed out before
all polls succeeded. The SUT is producing inconsistent reads.

**Remediation**:
- Increase `--settle-timeout` to allow more time.
- Increase `--settle-interval` if the SUT needs longer between polls.
- If the SUT's read-after-write consistency is genuinely unreliable, the
  scenario's numbers are inherently noisy — consider using `raw-consistency`
  (§ Scenarios) as the measurement target instead, which explicitly measures
  the visibility window.

### "abort watcher deafened by sample cap (ratio >2×)"

The abort watcher's rolling window can hold at most `--abort-window-max-samples`
samples. If `peak_rps × max_sustain` exceeds 2× this cap, samples roll out of
the window before they can be observed — the watcher can never fire.

**Remediation**: bump `--abort-window-max-samples` to at least
`2 × peak_rps × max_sustain`. Use `loadgen recommend --target-rps=N --duration=...`
to compute the recommended value automatically.

Example: at 500 rps with a 30s sustain window → `2 × 500 × 30 = 30000`.

```bash
loadgen recommend --target-rps=500 --duration=5m
# prints: --abort-window-max-samples=30000
```

## See also

- [USAGE.md → Pitfalls](../../USAGE.md#pitfalls--troubleshooting)
- [USAGE.md → Concepts → RUN QUALITY](../../USAGE.md#concepts)
- [`triage.sh`](../../scripts/triage.sh) for diagnostic data collection
- [`compare-runs.sh`](../../scripts/compare-runs.sh) to diff two artifact bundles
