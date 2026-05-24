# Production-readiness gate — `valkey-cluster`

**Status: NOT yet cleared for PROD as of 2026-05-24.**

Read this before deploying to any production cluster. Everything in
`runbooks/test-report.md` was validated on a single-host **kind** cluster
(kindnet CNI, local-path storage, no zones, no Prometheus Operator, no TLS).
The chart's *functional* behavior is well-tested there; the *infrastructure*
properties that matter most for a stateful datastore are **not**, because
kind can't exercise them.

This runbook is the gate between "works on kind" and "safe in PROD." Run
every **G-item** below on a **real managed cluster** (EKS/GKE/AKS) used as
staging, sign off each one, and make the two product decisions (G7, G8)
explicitly. Do not promote to PROD with any High-risk item unchecked.

> For Claude: this file is the source of truth for the pre-PROD gate. When
> the user is deploying to a real cluster, drive these checks in order, fill
> in the result/date columns, and refuse to call the chart prod-ready while
> any High item is unchecked. The functional checks (cluster forms, failover,
> rolling update, scale, persistence-across-pod-delete) are already PASS on
> kind — see `test-report.md`; don't re-litigate those, focus on G1–G8.

---

## Gate summary

| ID | Item | Risk | Status | Verified on / date |
|----|------|------|--------|--------------------|
| G1 | Real CSI volume: reattach across node failure | High | ☐ | |
| G2 | Multi-AZ spread + zone-locked volumes | High | ☐ | |
| G3 | NetworkPolicy actually enforced under a real CNI | High | ☐ | |
| G4 | Prometheus Operator picks up ServiceMonitor + rules | Medium | ☐ | |
| G5 | Long-running soak (memory/AOF/log drift) | Medium | ☐ | |
| G6 | `hard` anti-affinity has enough nodes (≥ masters×(1+replicas)) | Med | ☐ | |
| G7 | **Decision:** TLS / encryption-in-transit requirement | High | ☐ | |
| G8 | **Decision:** backup + tested restore (DR) | High | ☐ | |

Fill `Status` with ✅ / ❌ and the cluster + date as you go.

---

## Pre-gate: install on the staging (managed) cluster

```bash
# Pre-create namespace + auth secret (NEVER let the chart generate the password in prod)
kubectl create namespace valkey
kubectl -n valkey create secret generic valkey-cluster-auth \
  --from-literal=valkey-password="$(openssl rand -base64 32 | tr -d /+= | head -c 32)"

# Edit values-production.yaml first — every line marked TODO:
#   image.registry, persistence.storageClass, networkPolicy.allowedClients,
#   config.maxmemory (~70% of resources.limits.memory), metrics labels.
helm install valkey-cluster ./deploy/charts/valkey-cluster -n valkey \
  -f deploy/charts/valkey-cluster/values-production.yaml --wait --timeout 10m
helm test valkey-cluster -n valkey

PW=$(kubectl -n valkey get secret valkey-cluster-auth -o jsonpath='{.data.valkey-password}' | base64 -d)
```

Confirm baseline before testing anything else:
`cluster_state:ok`, `cluster_slots_assigned:16384`, `cluster_known_nodes:6`, `cluster_size:3`.

---

## G1 — Real CSI volume reattach across node failure  (High)

**Why:** kind used local-path. Real cloud block volumes (EBS / PD / Azure Disk)
are RWO and **zone-locked**. The dangerous failure mode: a master pod's node
dies, the pod reschedules onto a node in a *different* AZ, and the volume
**cannot attach** → pod stuck `ContainerCreating` / `FailedAttachVolume`
forever. `nodes.conf` lives on that volume, so this is also a recovery path.

**Test:**
```bash
# Pick a master pod and the node it's on
kubectl -n valkey get pod valkey-cluster-0 -o wide
NODE=$(kubectl -n valkey get pod valkey-cluster-0 -o jsonpath='{.spec.nodeName}')

# Write a sentinel key routed to that shard, then hard-fail the node
kubectl -n valkey exec valkey-cluster-0 -- valkey-cli -a "$PW" -c set gate:g1 alive
# Cordon + drain (or stop the underlying VM via the cloud console for a truer test)
kubectl drain "$NODE" --ignore-daemonsets --delete-emptydir-data --force
```

**Pass criteria:**
- The rescheduled pod reaches `Running 2/2` (volume re-attaches) within a few minutes — NOT stuck on `FailedAttachVolume`.
- `kubectl -n valkey exec valkey-cluster-0 -- valkey-cli -a "$PW" -c get gate:g1` returns `alive`.
- `cluster_state:ok` after settle. (Uncordon the node afterward: `kubectl uncordon "$NODE"`.)

**If it fails:** the volume is zone-locked and the pod landed in the wrong AZ.
This is why G2 matters — pods and their volumes must be pinned to the same zone.

---

## G2 — Multi-AZ spread + zone-locked volumes  (High)

**Why:** `values-production.yaml` sets a topology spread on
`topology.kubernetes.io/zone` but with `whenUnsatisfiable: ScheduleAnyway`
(soft). Soft means the scheduler *may* place two masters in one AZ — then one
AZ outage takes a whole shard (master + its replica) down. Combined with G1,
zone-locked volumes also constrain where a pod can ever reschedule.

**Test:**
```bash
# How are the 6 pods spread across zones?
kubectl -n valkey get pod -l app.kubernetes.io/name=valkey-cluster \
  -o custom-columns='POD:.metadata.name,NODE:.spec.nodeName' --no-headers \
| while read p n; do z=$(kubectl get node "$n" -o jsonpath='{.metadata.labels.topology\.kubernetes\.io/zone}'); echo "$p $z"; done
```

**Pass criteria:** masters land in distinct AZs and no AZ holds both a master
and its own replica. If they're lumped together, decide:
- flip the constraint to `whenUnsatisfiable: DoNotSchedule` (hard zone spread), **or**
- accept the risk explicitly and document it.

**Note:** hard zone spread + zone-locked RWO volumes means a pod can only ever
run in its volume's zone — verify your StorageClass uses
`volumeBindingMode: WaitForFirstConsumer` so the volume is created in the zone
the pod is scheduled to (not a random zone).

---

## G3 — NetworkPolicy actually enforced  (High, security)

**Why:** kindnet does NOT enforce NetworkPolicy, so the deny-by-default
boundary — the chart's primary network control — has **never been observed
working**. The manifest renders correctly; behavior is unconfirmed.

**Test (run on a CNI that enforces NP — Calico/Cilium):**
```bash
# From an ALLOWED namespace (one matching networkPolicy.allowedClients) — should CONNECT
kubectl -n chat-services run np-ok --rm -it --image=valkey/valkey:8.1.4-alpine --restart=Never -- \
  valkey-cli -h valkey-cluster-headless.valkey.svc.cluster.local -a "$PW" ping     # expect PONG

# From a NON-allowed namespace — should TIME OUT / be refused
kubectl create ns np-blocked 2>/dev/null
kubectl -n np-blocked run np-bad --rm -it --image=valkey/valkey:8.1.4-alpine --restart=Never -- \
  valkey-cli -h valkey-cluster-headless.valkey.svc.cluster.local -a "$PW" -t 5 ping  # expect timeout
```

**Pass criteria:** allowed namespace gets `PONG`; blocked namespace times out.
Confirm `kubectl -n valkey get networkpolicy` exists and your CNI enforces it
(check the CNI docs — installing Calico/Cilium alone isn't always enough on
managed clusters).

---

## G4 — Prometheus Operator integration  (Medium)

**Why:** ServiceMonitor + PrometheusRule were disabled on kind (no operator
CRDs). The prod values enable them with `release: prometheus` selector
labels — **placeholders** that must match your operator's `serviceMonitorSelector`.

**Test:**
```bash
kubectl -n valkey get servicemonitor,prometheusrule
# In Prometheus UI: Status → Targets → confirm the valkey-cluster pods are UP and scraped.
# Confirm the alert rules loaded: Status → Rules → search "valkey".
```

**Pass criteria:** targets show as scraped (not "0 up"), and the chart's alert
rules appear loaded. If targets are missing, your `metrics.serviceMonitor.labels`
don't match the operator's selector — fix the label, not the chart.

---

## G5 — Long-running soak  (Medium)

**Why:** no test has run longer than a few minutes. Memory drift, AOF file
growth on the PVC, and log volume over days are unknown.

**Test:** leave the staging cluster running under representative load for
**≥ 72h**. Watch:
```bash
# RSS / maxmemory headroom
kubectl -n valkey exec valkey-cluster-0 -- valkey-cli -a "$PW" info memory | grep -E "used_memory_human|maxmemory_human|mem_fragmentation_ratio"
# AOF size growth on the PVC
kubectl -n valkey exec valkey-cluster-0 -- sh -c 'du -sh /data/*'
```

**Pass criteria:** memory stays within `maxmemory`, fragmentation ratio stays
reasonable (< ~1.5), AOF rewrites keep file size bounded, no OOMKills
(`kubectl -n valkey get pod -o wide` restart counts stay 0), log volume sane.

---

## G6 — `hard` anti-affinity node count  (Medium)

**Why:** prod values set `podAntiAffinityPreset: hard` (one pod per node).
Total pods = `masters × (1 + replicasPerMaster)` = 6 by default. With < 6
schedulable worker nodes, the surplus pods sit **Pending** forever.

**Test / Pass criteria:**
```bash
kubectl get nodes --no-headers | grep -vc control-plane   # must be >= total pods (6 default)
kubectl -n valkey get pod -l app.kubernetes.io/name=valkey-cluster   # zero Pending
```
If you can't meet the node count, use `soft` and document the reduced
node-failure isolation, or shrink the topology.

---

## G7 — DECISION: TLS / encryption-in-transit  (High)

**State:** `auth.tls.enabled` is a **stub** — there is no TLS wiring in the
chart. Today: plaintext traffic protected only by the `requirepass` password.

**Decide:** is plaintext-with-password acceptable for your threat model
(in-cluster only, behind the NetworkPolicy from G3)?
- **Yes** → record the decision here and move on. The deny-by-default NP +
  in-cluster-only Service is the compensating control.
- **No** → this is a **blocking feature gap**, not a config flag. TLS must be
  implemented (server certs via cert-manager, `tls-port`, client trust,
  `masterauth` over TLS for replication) before PROD. Track as separate work.

**Sign-off:** _decision: ____________  by: ________  date: _________

---

## G8 — DECISION: backup + tested restore  (High)

**State:** the chart ships **no backup mechanism** (confirmed — no backup
CronJob template). `runbooks/disaster-recovery.md` documents restore *procedures*
(PVC snapshot restore, RDB restore with `cluster.init.enabled=false`), but
those paths have **never been tested against a real CSI driver**, and snapshot
creation is left to your infrastructure.

**Decide + verify (per the data class in disaster-recovery.md "Backup strategy"):**
- **Cache-only** → no backup; document that restore = reinstall + warm from
  source of truth. Done.
- **State/session/queue** → wire daily `VolumeSnapshot` of the PVCs via your
  CSI snapshot class, AND **actually run Scenario 2 from disaster-recovery.md
  end-to-end on staging** — snapshot, delete, restore onto same-named PVCs,
  reinstall with `cluster.init.enabled=false`, confirm data returned.
- **High-value** → additionally cron `BGSAVE` + copy `dump.rdb` to object
  storage, verify with `valkey-check-rdb`.

**Pass criteria:** a restore has been performed on staging and verified to
return real data — not just "the procedure is written down."

**Sign-off:** _data class: ______  restore tested: ____  by: ____  date: _____

---

## Promotion checklist

Promote to PROD only when:
- [ ] G1, G2, G3 (High infra) all ✅ on a managed cluster
- [ ] G7 TLS decision recorded (and implemented if "No")
- [ ] G8 backup decision recorded and, if applicable, a restore tested on staging
- [ ] G4, G5, G6 ✅ or explicitly risk-accepted with owner + date
- [ ] `helm test` passes on the staging cluster
- [ ] `values-production.yaml` has zero remaining `TODO` markers

Related runbooks: `failover.md` (single-pod), `disaster-recovery.md` (full
loss / restore), `scale-out.md` (manual resharding), `test-report.md`
(what's already PASS on kind).
