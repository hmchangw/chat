# Disaster recovery runbook

Covers: total cluster loss, restoring from PVC snapshots, restoring from
an RDB backup. For single-pod failure see `failover.md` — Valkey handles
that case automatically.

## Scenario 1: All pods down, PVCs intact

This is the common case (full namespace restart, cluster upgrade rolled
into oblivion). Recovery is automatic if `nodes.conf` is on the PVCs.

```bash
# Make sure PVCs are still bound
kubectl -n valkey get pvc -l app.kubernetes.io/name=valkey-cluster

# Scale the StatefulSet back up (if it was scaled to 0)
kubectl -n valkey scale sts valkey-cluster --replicas=6

# Wait for Ready
kubectl -n valkey rollout status sts/valkey-cluster --timeout=10m

# Verify cluster reformed itself from nodes.conf
PW=$(kubectl -n valkey get secret valkey-cluster-auth -o jsonpath='{.data.valkey-password}' | base64 -d)
kubectl -n valkey exec valkey-cluster-0 -- valkey-cli -a "$PW" cluster info
```

Each pod reads its `nodes.conf` from `/data`, recognizes the cluster
topology, and gossips with peers via the headless Service DNS. Within
~30s of all pods being Ready, `cluster_state:ok` should return.

**Do not run the cluster-create Job in this scenario.** It is idempotent
(it skips if `cluster_slots_assigned >= 16384`), but running it
manually is unnecessary.

## Scenario 2: All PVCs lost, but you have an RDB backup

If volume snapshots / external backups exist, restore them onto fresh
PVCs of the same names (`data-valkey-cluster-0`, `-1`, ... `-5`) before
scaling the StatefulSet up. Procedure depends on your CSI driver — for
most:

```bash
# Helm uninstall releases the StatefulSet but does NOT delete PVCs
helm uninstall valkey-cluster -n valkey

# Restore each PVC from snapshot (example shape - adapt to your snapshot class)
for i in 0 1 2 3 4 5; do
  kubectl -n valkey apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: data-valkey-cluster-$i
spec:
  storageClassName: <your-sc>
  dataSource:
    name: data-valkey-cluster-$i-<snapshot-id>
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 8Gi
EOF
done

# Reinstall the chart - StatefulSet will adopt the existing PVCs
helm install valkey-cluster ./deploy/charts/valkey-cluster -n valkey \
  --set auth.existingSecret=valkey-cluster-auth \
  --set cluster.init.enabled=false   # IMPORTANT: skip CLUSTER CREATE
```

`cluster.init.enabled=false` is critical here. The restored `nodes.conf`
files already describe a cluster; running `CLUSTER CREATE` against nodes
that already think they're in a cluster will fail with `ERR Slot 0 is
already busy`.

After pods are Ready, re-enable init for the next deploy:

```bash
helm upgrade valkey-cluster ./deploy/charts/valkey-cluster -n valkey \
  --reuse-values --set cluster.init.enabled=true
```

(The Job is idempotent so this won't do anything destructive.)

## Scenario 3: Total loss (no PVCs, no backup) — bootstrapping from scratch

You're starting over. Data is gone. If your application can tolerate an
empty cache, this is fine. If it can't, you need a different backup
strategy — see "Backup strategy" below.

```bash
# Full clean slate
helm uninstall valkey-cluster -n valkey
kubectl -n valkey delete pvc -l app.kubernetes.io/name=valkey-cluster

# Reinstall fresh
helm install valkey-cluster ./deploy/charts/valkey-cluster -n valkey \
  --set auth.existingSecret=valkey-cluster-auth
```

## Scenario 4: Split-brain after a long network partition

Rare in single-cluster deployments but possible. If two halves of the
cluster diverge:

```bash
# Identify which side has the majority of masters
kubectl -n valkey exec valkey-cluster-0 -- valkey-cli -a "$PW" cluster nodes
```

Pick the majority side. On the minority side, for each master that
should stop accepting writes:

```bash
# On a replica on the majority side that you want to make master
kubectl -n valkey exec <majority-replica> -- valkey-cli -a "$PW" CLUSTER FAILOVER FORCE
```

If `FORCE` doesn't work because the minority is partitioned away, use
`TAKEOVER` (last resort — can cause divergence):

```bash
kubectl -n valkey exec <replica> -- valkey-cli -a "$PW" CLUSTER FAILOVER TAKEOVER
```

After the partition heals, the minority-side nodes will see the new
topology and rejoin as replicas. Run `CLUSTER RESET HARD` on any node
that refuses to rejoin (it forgets all cluster state and re-meets fresh).

## Backup strategy

The chart does not ship one. Decide based on data criticality:

- **Cache-only workload** — no backup needed. Restore = reinstall + warm
  the cache from source of truth.
- **State / queue / session** — enable AOF (`config.appendOnly: true`,
  already the chart default) and snapshot PVCs daily via your CSI
  driver's VolumeSnapshot. Test restores quarterly.
- **High-value data** — additionally cron a `BGSAVE` and copy `dump.rdb`
  to object storage (S3/GCS) every N minutes. Verify integrity with
  `valkey-check-rdb` before relying on it.
