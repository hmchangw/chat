# Failover runbook

## What automatic failover looks like

When a master pod dies (node failure, OOM, explicit `kubectl delete pod`):

1. Other masters in the cluster detect the failure via gossip after
   `cluster-node-timeout` (default 15s in this chart).
2. The dead master's replicas race to be promoted; one wins via a vote
   among the remaining masters.
3. The winning replica becomes the new master and takes over the slot
   range. Total client-visible unavailability: typically 15–30s.
4. Kubernetes reschedules the dead pod. When it returns, it reads the
   persisted `nodes.conf` from its PVC, sees that its old master role
   was taken, and rejoins as a replica of the new master.

No chart code is involved — this is pure Valkey gossip + PVC persistence.

## Verifying

```bash
PW=$(kubectl -n valkey get secret valkey-cluster-auth -o jsonpath='{.data.valkey-password}' | base64 -d)

# Which pods are masters right now?
kubectl -n valkey exec valkey-cluster-0 -- valkey-cli -a "$PW" cluster nodes \
  | awk '/master/ {print $2, $3}'
```

## Manual failover (controlled handoff)

Sometimes you want to demote a master without killing it — e.g., to drain
its node, or to test the failover path. From any pod attached to the
master you want to demote:

```bash
kubectl -n valkey exec valkey-cluster-3 -- valkey-cli -a "$PW" CLUSTER FAILOVER
```

This is what the chart's `preStop` hook does automatically when a master
pod is terminating gracefully.

Flags:
- `CLUSTER FAILOVER` — coordinated failover. Default. Brief data freeze
  during handoff, then the replica is promoted.
- `CLUSTER FAILOVER FORCE` — replica promotes even if it can't reach the
  master. Use only if the master is genuinely unreachable.
- `CLUSTER FAILOVER TAKEOVER` — replica seizes the role without quorum.
  Last-resort tool for split-brain recovery. Can introduce inconsistency.

## When failover does NOT happen

- All replicas of a master are also down → that slot range is unreachable
  until either the master or one replica comes back. Cluster reports
  `cluster_state:fail` (with `require-full-coverage=yes`, which is the
  default) and refuses writes to any node.
- Network partition that splits the cluster bus → minority side stops
  serving writes; majority side stays up. Reconciles automatically when
  the partition heals.
- Quorum lost (more than half of masters down) → no failover votes
  succeed; cluster effectively halts.

## What to check during/after failover

```bash
# Cluster state - should return to ok within ~30s
kubectl -n valkey exec valkey-cluster-0 -- valkey-cli -a "$PW" cluster info

# Per-master replication status
for i in 0 1 2 3 4 5; do
  echo "=== pod $i ==="
  kubectl -n valkey exec valkey-cluster-$i -- valkey-cli -a "$PW" info replication \
    | grep -E '^(role|connected_slaves|master_link_status):'
done
```

## What the alerts will tell you

If you enabled `metrics.prometheusRule`:
- `ValkeyClusterStateNotOk` fires within 1m of `cluster_state != ok`.
- `ValkeyMasterMissingReplica` fires after 10m if a master loses its
  replica and a new one isn't placed.
- `ValkeyReplicationLagHigh` fires if replication lag exceeds 30s.
