# valkey-cluster

In-house Helm chart that deploys [Valkey](https://valkey.io) in cluster mode on
the upstream `valkey/valkey:8.1.4-alpine` image. Built to replace the
unmaintained `bitnamilegacy/valkey-cluster` image without introducing a paid
subscription or an operator.

Scope: same-cluster clients only, in-cluster traffic, no external access.
Same-cluster boundary is enforced at runtime by a NetworkPolicy.

## Layout

```
deploy/charts/valkey-cluster/
├── Chart.yaml                  # chart metadata (appVersion = Valkey version)
├── values.yaml                 # documented configuration knobs
├── templates/
│   ├── _helpers.tpl            # name/label/image helpers
│   ├── NOTES.txt               # post-install summary + connection hints
│   ├── serviceaccount.yaml
│   ├── secret.yaml             # only created when auth.existingSecret is empty
│   ├── configmap.yaml          # valkey.conf + entrypoint/probe/preStop scripts
│   ├── service-headless.yaml   # stable per-pod DNS for clients and gossip
│   ├── service-metrics.yaml    # ClusterIP service for the exporter sidecar
│   ├── statefulset.yaml        # data-plane pods with PVCs
│   ├── pdb.yaml                # disruption budget (maxUnavailable=1)
│   ├── networkpolicy.yaml      # deny-all client ingress unless allowedClients set
│   ├── job-cluster-create.yaml # post-install: idempotent CLUSTER CREATE
│   ├── job-cluster-scale.yaml  # post-upgrade: add-node + rebalance on scale-up
│   ├── servicemonitor.yaml     # Prometheus Operator scrape config (opt-in)
│   ├── prometheusrule.yaml     # default alert rules (opt-in)
│   └── tests/
│       └── connection-test.yaml  # helm test pod
├── runbooks/
│   ├── scale-out.md            # how to add masters manually if needed
│   ├── failover.md             # what happens during failover and how to verify
│   └── disaster-recovery.md    # recovering from total-cluster loss
└── README.md
```

## Install

```bash
kubectl create namespace valkey

# Pre-create the auth Secret so `helm upgrade` does not rotate the password.
kubectl -n valkey create secret generic valkey-cluster-auth \
  --from-literal=valkey-password="$(openssl rand -base64 32 | tr -d /+= | head -c 32)"

helm install valkey-cluster ./deploy/charts/valkey-cluster \
  --namespace valkey \
  --set auth.existingSecret=valkey-cluster-auth \
  --set config.maxmemory=1500mb \
  --wait --timeout 10m

# Verify
helm test valkey-cluster --namespace valkey
```

## Connecting from in-cluster services

Cluster-aware clients only need one seed address — they discover the rest via
`CLUSTER SLOTS`:

```go
rdb := redis.NewClusterClient(&redis.ClusterOptions{
    Addrs:    []string{"valkey-cluster-headless.valkey.svc.cluster.local:6379"},
    Password: os.Getenv("VALKEY_PASSWORD"),
})
```

Until `networkPolicy.allowedClients` lists their selector, client pods will be
blocked. This is intentional — explicit opt-in.

```yaml
networkPolicy:
  allowedClients:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: chat-services
    - podSelector:
        matchLabels:
          app.kubernetes.io/part-of: chat
```

## Scaling

### Vertical (resize pods)

Edit `resources.requests` / `resources.limits` in values, `helm upgrade`.
StatefulSet rolls one pod at a time, gracefulShutdown preStop hook ensures
each master fails over to a replica before terminating.

### Add a shard (scale-out)

Increase `cluster.masters` (and keep `cluster.replicasPerMaster`) in values:

```bash
helm upgrade valkey-cluster ./deploy/charts/valkey-cluster \
  --reuse-values --set cluster.masters=4
```

The `cluster-scale` post-upgrade Job:
1. Waits for new pods to be Ready.
2. Adds the new master(s) via `valkey-cli --cluster add-node`.
3. Attaches the new replica(s) to balance.
4. Runs `valkey-cli --cluster rebalance` (online slot migration).

For the manual procedure (if scale automation is disabled or you want to do
it by hand), see `runbooks/scale-out.md`.

### Scale-in (remove a shard)

Not automated. See `runbooks/scale-down.md` (not yet written — open an issue
if you need this).

## Verifying cluster health

```bash
PW=$(kubectl -n valkey get secret valkey-cluster-auth \
       -o jsonpath='{.data.valkey-password}' | base64 -d)

kubectl -n valkey exec valkey-cluster-0 -- valkey-cli -a "$PW" cluster info
# expect: cluster_state:ok / cluster_slots_assigned:16384 / cluster_known_nodes:6 / cluster_size:3

kubectl -n valkey exec valkey-cluster-0 -- valkey-cli -a "$PW" cluster nodes
# expect: 3 lines marked master, 3 lines marked slave, each slave -> a master
```

## What this chart deliberately omits

- TLS in transit. Wiring stub exists at `auth.tls.enabled`; not implemented
  yet. Add only if compliance requires it.
- Per-pod NodePort/LoadBalancer Services for external access. The chart
  assumes in-cluster clients only.
- HostNetwork mode, scheduler-name overrides, custom DNS config.
- Sentinel-bridge migration mode.

If you need any of these, raise it in a chart PR — the values structure
leaves room.

## CVE & maintenance posture

- Data-plane image: upstream `valkey/valkey:8.1.4-alpine`. Patch by bumping
  `image.tag` and running `helm upgrade`.
- Metrics image: `oliver006/redis_exporter` (community-maintained). Bump
  `metrics.image.tag` to patch.
- No Bitnami dependency, no SBOM patching, no Go binaries from third parties
  to track.
