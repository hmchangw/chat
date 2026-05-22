# Scale-out runbook (add a shard manually)

Use this when `cluster.scaleAutomation.enabled=false`, when the post-upgrade
Job fails partway, or when you want to drive the migration by hand for
auditability.

## Prerequisites

- Cluster currently has `M` masters and `M * (1 + R)` total pods.
- You want to grow to `M+1` masters and `(M+1) * (1 + R)` total pods.
- `valkey-cli` on hand, either in a pod or locally.
- Password handy: `PW=$(kubectl -n valkey get secret valkey-cluster-auth -o jsonpath='{.data.valkey-password}' | base64 -d)`.

## Steps

### 1. Scale the StatefulSet

```bash
kubectl -n valkey scale sts valkey-cluster --replicas=$NEW_TOTAL
```

Wait for all new pods to be Ready:

```bash
kubectl -n valkey rollout status sts/valkey-cluster --timeout=5m
```

### 2. Add the new master

The new pods are named `valkey-cluster-<idx>` where `idx >= current_total`.
Pick the first new pod to be the master:

```bash
NS=valkey
SVC=valkey-cluster-headless
NEW_MASTER_IDX=6   # if you went 6 -> 8 pods

kubectl -n $NS exec valkey-cluster-0 -- valkey-cli -a "$PW" --cluster add-node \
  valkey-cluster-${NEW_MASTER_IDX}.${SVC}.${NS}.svc.cluster.local:6379 \
  valkey-cluster-0.${SVC}.${NS}.svc.cluster.local:6379
```

### 3. Find the new master's node ID

```bash
NEW_MASTER_ID=$(kubectl -n $NS exec valkey-cluster-0 -- valkey-cli -a "$PW" \
  CLUSTER NODES | awk '/valkey-cluster-'$NEW_MASTER_IDX'/ {print $1}')
echo "$NEW_MASTER_ID"
```

### 4. Add the new replica(s) attached to the new master

```bash
NEW_REPLICA_IDX=7

kubectl -n $NS exec valkey-cluster-0 -- valkey-cli -a "$PW" --cluster add-node \
  --cluster-slave --cluster-master-id "$NEW_MASTER_ID" \
  valkey-cluster-${NEW_REPLICA_IDX}.${SVC}.${NS}.svc.cluster.local:6379 \
  valkey-cluster-0.${SVC}.${NS}.svc.cluster.local:6379
```

Repeat for additional replicas if `replicasPerMaster > 1`.

### 5. Rebalance slots (online — no downtime)

```bash
kubectl -n $NS exec valkey-cluster-0 -- valkey-cli -a "$PW" --cluster rebalance \
  --cluster-use-empty-masters \
  --cluster-pipeline 100 \
  valkey-cluster-0.${SVC}.${NS}.svc.cluster.local:6379
```

### 6. Verify

```bash
kubectl -n $NS exec valkey-cluster-0 -- valkey-cli -a "$PW" cluster info
# expect cluster_state:ok, cluster_slots_assigned:16384, cluster_size matches new master count

kubectl -n $NS exec valkey-cluster-0 -- valkey-cli -a "$PW" cluster nodes
# expect new masters and replicas present and not failed
```

## Rollback if step 5 fails partway

`valkey-cli --cluster rebalance` is online and resumable. If it stops with an
error, fix the underlying cause (network, OOM on a node) and re-run the same
command — it will pick up from current slot ownership and continue moving.

## Notes

- Do not run multiple reshards concurrently — the cluster serializes slot
  migrations and concurrent attempts will fail with `ERR Target instance ...`.
- The rebalance step requires `--cluster-use-empty-masters` because the new
  master starts with zero slots; without that flag, rebalance refuses to
  send slots to it.
