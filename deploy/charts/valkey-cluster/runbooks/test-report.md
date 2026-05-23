# Runtime test report — `valkey-cluster` chart

Date: 2026-05-23  
Chart version: 0.1.0  
Chart commit at test time: `98057c6` (pre-fix) → updated to fix bug found here  
Image under test: `valkey/valkey:8.1.4-alpine`, `oliver006/redis_exporter:v1.66.0-alpine`

## Why no Kubernetes cluster

I attempted to bring up a `kind` (v1.30.0) cluster in the test environment.
Kind's node container needs nested cgroup + namespace operations that the
sandboxed Docker runtime (vfs storage driver, cgroup v1, no privileged
exec) cannot provide:

```
ERROR: failed to init node with kubeadm: ... OCI runtime exec failed:
exec failed: unable to start container process: error executing setns
process: exit status 1
```

Rather than block testing entirely, I built an equivalent runtime test:
six `valkey/valkey:8.1.4-alpine` containers on a Docker bridge network,
running the **actual shell scripts the chart renders** (entrypoint,
liveness, readiness, preStop, cluster-create Job, scale-up Job, helm
test pod). This is not a substitute for a K8s integration test, but it
covers all the chart's runtime logic that isn't K8s-API-specific.

What is and isn't covered:

| Category | Tested here | Needs real K8s |
|---|---|---|
| Helm template render | ✅ helm lint, helm template across 6 value sets | — |
| K8s schema validity | ✅ kubeconform strict against 1.29 + 1.30 + CRDs | — |
| Shell scripts | ✅ shellcheck + live execution against Valkey | — |
| `valkey.conf` rendering | ✅ exact rendered config, verified Valkey starts cleanly | — |
| Cluster bootstrap (CLUSTER CREATE) | ✅ ran rendered Job script | — |
| Idempotency of bootstrap | ✅ re-ran, hit "already initialized" branch | — |
| Failover via `preStop` | ✅ found and fixed a real bug here | — |
| Persistent restart rejoin | ✅ stop+restart with same `/data` → same node ID | — |
| Scale-up (add-node + rebalance) | ✅ 6→8 nodes, 3→4 masters, slots migrated online | — |
| `helm test` script | ✅ PASS line emitted | — |
| Metrics sidecar | ✅ all PrometheusRule metrics present | — |
| Selector/label consistency | ✅ verified across STS / svc / PDB / NetworkPolicy | — |
| NetworkPolicy enforcement | partial: rendered shape verified | ⚠ needs real K8s + CNI |
| StatefulSet rolling update | — | ⚠ needs real K8s |
| PVC `volumeClaimTemplates` | — | ⚠ needs real K8s |
| Helm hook ordering (post-install, post-upgrade) | — | ⚠ needs real K8s |
| ServiceMonitor scrape by Prometheus Operator | — | ⚠ needs real K8s |

## Bug found and fixed during these tests

### 🐛 `pre-stop.sh` sent `CLUSTER FAILOVER` to the wrong target

The original script invoked `CLUSTER FAILOVER` against itself
(`vc CLUSTER FAILOVER` resolves to `valkey-cli -h 127.0.0.1 ... FAILOVER`).
Valkey rejects this with `ERR You should send CLUSTER FAILOVER to a replica`.

**Symptom observed:**

```
preStop: triggering CLUSTER FAILOVER for master valkey-0
ERR You should send CLUSTER FAILOVER to a replica
exit=0
```

After the script "completed," the master's role stayed `master` —
no failover happened. In production this would mean every master pod
termination would briefly drop writes to its slots while gossip
detected the death.

**Fix:** rewrote `pre-stop.sh` to:
1. Check role; replicas exit immediately (nothing to do).
2. On masters: read `CLUSTER MYID`, find a healthy replica whose
   `<master>` column equals that ID via `CLUSTER NODES`.
3. Parse the replica's `host:port` from the `<ip:port@bus,hostname>` field.
4. Send `CLUSTER FAILOVER` to *that replica*.
5. Poll local `INFO replication` until role flips to `slave`
   (bounded by `gracefulShutdown.timeoutSeconds`).

**Verification of fix:**

```
preStop: valkey-0 is a master (id=47835960eff7b21122744b4003c7df431ca8c64d), looking for a replica to promote
preStop: triggering CLUSTER FAILOVER on replica 172.20.0.6:6379
OK
preStop: failover complete, valkey-0 is now a replica
exit=0
```

After: valkey-0 was a `slave`, valkey-4 (its previous replica) was the
new `master`, `cluster_state` remained `ok` throughout.

## Test matrix executed

### T1 — Cluster bootstrap

Six valkey-server containers on `valkey-test` docker network. Ran the
chart's rendered `job-create.sh` script.

**Result:** ✅ PASS

```
>>> Performing Cluster Check (using node valkey-0...:6379)
M: 47835960... valkey-0...:6379  slots:[0-5460]      (5461 slots) master  1 additional replica(s)
M: 2017e133... valkey-1...:6379  slots:[5461-10922]  (5462 slots) master  1 additional replica(s)
M: 85433380... valkey-2...:6379  slots:[10923-16383] (5461 slots) master  1 additional replica(s)
S: 7540f624... slave (replicates 47835960...)
S: a9c480d2... slave (replicates 85433380...)
S: 462cf9c4... slave (replicates 2017e133...)
[OK] All nodes agree about slots configuration.
[OK] All 16384 slots covered.
```

After ~5s gossip convergence:

```
cluster_state:ok
cluster_slots_assigned:16384
cluster_slots_ok:16384
cluster_known_nodes:6
cluster_size:3
```

### T2 — Bootstrap idempotency

Re-ran `job-create.sh` against the same healthy cluster. Expected: no-op
exit (the script's `slots >= 16384` guard).

**Result:** ✅ PASS

```
cluster already initialized (cluster_slots_assigned=16384), skipping CLUSTER CREATE
```

### T3 — helm test script

Ran rendered `helm-test.sh` against the live cluster (expected_masters=3,
expected_total=6).

**Result:** ✅ PASS

```
PASS: cluster_state=ok slots=16384 known=6 size=3 set/get round-trip ok
```

### T4 — preStop failover (original)

**Result:** ❌ FAIL — see "Bug found and fixed" above.

### T4' — preStop failover (after fix)

**Result:** ✅ PASS

Before: valkey-0=master, valkey-4=slave  
After running fixed pre-stop.sh on valkey-0: valkey-0=slave, valkey-4=master  
cluster_state stayed `ok`.

### T5 — Scale-up

Pre-condition: 6 nodes (3+3). Started two new containers (`valkey-6`,
`valkey-7`). Ran rendered `job-scale.sh` with DESIRED_TOTAL=8 DESIRED_MASTERS=4.

**Result:** ✅ PASS

The script:
1. Detected current=6 known / 3 masters; desired=8 known / 4 masters.
2. Added `valkey-6` as a new master (no slots).
3. Added `valkey-7` as a replica.
4. Ran `valkey-cli --cluster rebalance --cluster-use-empty-masters`,
   which moved 1365 slots from each of two existing masters to the new
   master. (≈ 4096/3 expected fair-share.)
5. Final state:

```
cluster_state:ok
cluster_slots_assigned:16384
cluster_slots_ok:16384
cluster_known_nodes:8
cluster_size:4
```

### T6 — Pod restart with persisted data

Added `valkey-9` to the cluster as a replica with `/data` mounted on a
host directory. Confirmed `nodes.conf` written. `docker stop` + `docker
rm`, then re-created the container with the same `/data` volume.

**Result:** ✅ PASS — pod returned with the **same node ID**
(`75bf0eb00cf63799e83d5282246abd964dcbdc89`) and rejoined the cluster
automatically. `cluster_state` stayed `ok` throughout.

### T7 — Metrics exporter

Ran `oliver006/redis_exporter:v1.66.0-alpine` as a sidecar pointing at
`valkey-4` (a master). Scraped `/metrics`.

**Result:** ✅ PASS — every metric referenced by the chart's
`PrometheusRule` is present:

| Metric | Value observed |
|---|---|
| `redis_up` | 1 |
| `redis_cluster_state` | 1 (ok) |
| `redis_cluster_slots_assigned` | 16384 |
| `redis_cluster_slots_ok` | 16384 |
| `redis_cluster_slots_fail` | 0 |
| `redis_cluster_known_nodes` | 8 |
| `redis_cluster_size` | 4 |
| `redis_instance_info` | role-labeled, all fields present |
| `redis_memory_used_bytes` | ~3.8 MB |
| `redis_memory_max_bytes` | 200 MB (matches `config.maxmemory=200mb`) |
| `redis_connected_slave_lag_seconds` | 1s (labeled per-slave) |
| `redis_rejected_connections_total` | 0 |

### T8 — No-auth render

`helm template --set auth.enabled=false`.

**Result:** ✅ PASS — no `Secret` resource rendered, no `VALKEY_PASSWORD`
env var anywhere (StatefulSet, Jobs, helm test Pod).

### T9 — NetworkPolicy shape

`helm template` with `networkPolicy.allowedClients` set to a
namespaceSelector + podSelector.

**Result:** ✅ PASS — three ingress rules, three egress rules, all aligned:

- Ingress 0: intra-release pods → ports 16379 (bus) + 6379 (client)
- Ingress 1: declared client selectors → port 6379
- Ingress 2: any namespace → port 9121 (metrics)
- Egress 0: kube-dns
- Egress 1: intra-release pods → 6379 + 16379

Note: actual enforcement requires a CNI that implements NetworkPolicy
(Calico, Cilium, etc.); this verification only confirms the manifest
shape.

### T10 — TLS stub

`helm template --set auth.tls.enabled=true`.

**Result:** ✅ PASS — renders identically to the disabled state.
Confirmed the stub is a values toggle only, not wired into any
template yet (as documented).

### T11 — Scale automation disabled

`helm template --set cluster.scaleAutomation.enabled=false`.

**Result:** ✅ PASS — exactly 1 Job rendered (cluster-create), no
cluster-scale Job.

## Static analysis (re-run after fix)

| Check | Result |
|---|---|
| `helm lint` | clean (1 info: missing icon) |
| `helm template` × 6 value sets | all render |
| `kubeconform -strict` k8s 1.29 | 13/13 valid |
| `kubeconform -strict` k8s 1.30 | 13/13 valid |
| `shellcheck -s sh` × 7 scripts | clean |
| `sh -n` syntax check | clean |
| Selector/label consistency | aligned across STS/svc/PDB/NetworkPolicy |
| Resource name length (long-release test) | all ≤ 63 chars; Jobs ≤ 57 |
| `redis_exporter` metric names | all 12 referenced metrics verified present |

## What still needs a real K8s cluster

These can only be exercised in K8s and should be part of staging
acceptance:

1. **NetworkPolicy enforcement** by a CNI plugin. The manifest is
   shaped correctly; whether traffic is actually filtered depends on
   the cluster's CNI.
2. **StatefulSet rolling update** — `helm upgrade --set image.tag=X`
   should drain pods one at a time, each pod running its preStop hook.
   We verified the script works in isolation; the orchestration around
   it is K8s-native.
3. **PVC volumeClaimTemplates** — Helm install creates PVCs sized per
   `persistence.size`. We verified the rejoin-on-restart behavior with a
   bind-mount; PVC retention across pod restarts is a stock StatefulSet
   guarantee.
4. **Helm hook ordering** — `post-install` for cluster-create,
   `post-upgrade` for scale. Hook timing relative to pod readiness
   needs a real cluster; the scripts themselves already poll for
   readiness before doing work.
5. **ServiceMonitor pickup by Prometheus Operator** — the manifest
   is schema-valid (kubeconform with CRDs); whether prometheus-operator
   actually selects and scrapes it needs a real prom-operator install.
6. **Long-running stability** — memory leaks, log volume, replication
   lag drift over hours/days. Out of scope for a sandbox test.

## Recommended next step before prod

Run these against your dev cluster (NOT prod):

```bash
helm install valkey-test ./deploy/charts/valkey-cluster \
  --namespace valkey-test --create-namespace \
  --set auth.password=devpass \
  --set config.maxmemory=500mb \
  --set networkPolicy.enabled=false \
  --wait --timeout 10m

helm test valkey-test --namespace valkey-test

# Failover smoke
kubectl -n valkey-test delete pod <release>-valkey-cluster-0
kubectl -n valkey-test wait --for=condition=Ready pod/<release>-valkey-cluster-0 --timeout=60s
kubectl -n valkey-test exec <release>-valkey-cluster-0 -- \
  valkey-cli -a devpass cluster info | grep state

# Scale smoke
helm upgrade valkey-test ./deploy/charts/valkey-cluster --reuse-values \
  --set cluster.masters=4
kubectl -n valkey-test wait --for=condition=Complete \
  job -l app.kubernetes.io/component=cluster-scale --timeout=5m

# Tear down
helm uninstall valkey-test --namespace valkey-test
kubectl -n valkey-test delete pvc -l app.kubernetes.io/name=valkey-cluster
```

If any of T1-T11 fail in your cluster but passed here, the most likely
causes are CNI / storage / RBAC differences that aren't visible in this
sandbox.
