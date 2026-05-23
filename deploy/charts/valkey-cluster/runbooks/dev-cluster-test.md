# Dev cluster testing runbook

Step-by-step instructions for standing up a local **kind** cluster on your
laptop, deploying the `valkey-cluster` chart, and exercising every feature.
Designed so you can copy-paste each block and see expected output.

**Time budget:** ~30 min if everything goes right, ~60 min with debugging.

**System requirements:**
- Linux (Ubuntu/Debian/Fedora) or macOS (Intel or Apple Silicon)
- 6+ GB free RAM (3 kind worker nodes × ~1.5 GB)
- 20+ GB free disk
- Internet access to Docker Hub (or a mirror)

---

## 0. Prerequisites — install Docker, kind, kubectl, helm

### Linux (Ubuntu 22.04+)

```bash
# Docker
sudo apt-get update
sudo apt-get install -y docker.io
sudo usermod -aG docker "$USER"
newgrp docker
docker version --format '{{.Server.Version}}'   # expect 20.10+

# kind
KIND_VERSION=v0.24.0
curl -fsSLo /tmp/kind https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64
sudo install -m 0755 /tmp/kind /usr/local/bin/kind
kind version                                     # expect kind v0.24.0

# kubectl
KUBECTL_VERSION=v1.30.4
curl -fsSLo /tmp/kubectl https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl
sudo install -m 0755 /tmp/kubectl /usr/local/bin/kubectl
kubectl version --client --short                 # expect Client Version: v1.30.4

# helm
HELM_VERSION=v3.16.4
curl -fsSLo /tmp/helm.tgz https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz
tar -xzf /tmp/helm.tgz -C /tmp
sudo install -m 0755 /tmp/linux-amd64/helm /usr/local/bin/helm
helm version --short                             # expect v3.16.4
```

### macOS

```bash
brew install --cask docker          # then start Docker Desktop
brew install kind kubectl helm

kind version && kubectl version --client --short && helm version --short
```

### Behind a corporate proxy

Set `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` env vars for your shell, and add the same to Docker's daemon settings (`/etc/docker/daemon.json` or Docker Desktop → Settings → Resources → Proxies). Restart Docker after.

---

## 1. Create the kind cluster

We want 3 worker nodes so the chart's hard pod anti-affinity (`podAntiAffinityPreset: hard`) can spread 3 masters across distinct nodes.

```bash
cat > /tmp/kind-valkey.yaml <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: valkey-test
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
  - role: worker
  - role: worker
  - role: worker
EOF

kind create cluster --config /tmp/kind-valkey.yaml --image kindest/node:v1.30.0 --wait 5m
```

**Expected:**
```
Creating cluster "valkey-test" ...
 ✓ Ensuring node image (kindest/node:v1.30.0)
 ✓ Preparing nodes
 ✓ Writing configuration
 ✓ Starting control-plane
 ✓ Installing CNI
 ✓ Installing StorageClass
 ✓ Joining worker nodes
 ✓ Waiting ≤ 5m0s for control-plane = Ready
Set kubectl context to "kind-valkey-test"
```

Verify:
```bash
kubectl get nodes
# NAME                          STATUS   ROLES           AGE   VERSION
# valkey-test-control-plane     Ready    control-plane   60s   v1.30.0
# valkey-test-worker            Ready    <none>          50s   v1.30.0
# valkey-test-worker2           Ready    <none>          50s   v1.30.0
# valkey-test-worker3           Ready    <none>          50s   v1.30.0

kubectl get storageclass
# NAME                 PROVISIONER             RECLAIMPOLICY   ...
# standard (default)   rancher.io/local-path   Delete          ...
```

If `kind create cluster` fails on `Failed to mount cgroup at /sys/fs/cgroup/systemd`, you're on cgroup v1 host. Fix on Linux: switch the host to cgroup v2 by adding `systemd.unified_cgroup_hierarchy=1` to your kernel cmdline (`/etc/default/grub` → `GRUB_CMDLINE_LINUX`) and reboot.

---

## 2. Pre-load images into kind

Faster (and works in air-gapped environments) than letting each pod pull from Docker Hub:

```bash
# Pull on host
for img in \
  valkey/valkey:8.1.4-alpine \
  oliver006/redis_exporter:v1.66.0-alpine \
; do
  docker pull "$img"
done

# Load into all kind nodes
for img in \
  valkey/valkey:8.1.4-alpine \
  oliver006/redis_exporter:v1.66.0-alpine \
; do
  kind load docker-image "$img" --name valkey-test
done
```

**Expected** (per image):
```
Image: "valkey/valkey:8.1.4-alpine" with ID "sha256:..." not yet present on node "valkey-test-worker3", loading...
Image: "valkey/valkey:8.1.4-alpine" with ID "sha256:..." not yet present on node "valkey-test-worker2", loading...
...
```

Verify on a node:
```bash
docker exec valkey-test-worker crictl images | grep valkey
# docker.io/valkey/valkey       8.1.4-alpine    ...
```

---

## 3. Get the chart

```bash
cd ~
git clone https://github.com/hmchangw/chat.git
cd chat
git checkout claude/custom-valkey-cluster-chart

ls deploy/charts/valkey-cluster/
# Chart.yaml  COMPARISON.md  README.md  runbooks/  templates/
# values-production.yaml  values.yaml
```

Lint locally before deploying:
```bash
helm lint deploy/charts/valkey-cluster
# ==> Linting deploy/charts/valkey-cluster
# [INFO] Chart.yaml: icon is recommended
# 1 chart(s) linted, 0 chart(s) failed
```

---

## 4. Prepare auth Secret

Pre-create so `helm upgrade` doesn't rotate the password.

```bash
kubectl create namespace valkey

# Generate a strong password and store it locally for later commands
VALKEY_PW="$(openssl rand -base64 32 | tr -d /+= | head -c 32)"
echo "$VALKEY_PW" > /tmp/valkey-pw.txt
chmod 600 /tmp/valkey-pw.txt
echo "VALKEY_PW saved to /tmp/valkey-pw.txt"

kubectl -n valkey create secret generic valkey-cluster-auth \
  --from-literal=valkey-password="$VALKEY_PW"

kubectl -n valkey get secret valkey-cluster-auth
# NAME                  TYPE     DATA   AGE
# valkey-cluster-auth   Opaque   1      3s
```

---

## 5. Install the chart

Use a values file tailored for local kind (smaller resources, no Prom-Operator):

```bash
cat > /tmp/values-kind.yaml <<'EOF'
auth:
  existingSecret: valkey-cluster-auth
config:
  maxmemory: "100mb"
  maxmemoryPolicy: "noeviction"
resources:
  requests: { cpu: "100m", memory: "256Mi" }
  limits:   { cpu: "500m", memory: "512Mi" }
persistence:
  enabled: true
  size: "1Gi"
podAntiAffinityPreset: "hard"
pdb:
  enabled: true
  maxUnavailable: 1
networkPolicy:
  enabled: true
  allowedClients:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: valkey
metrics:
  enabled: true
  serviceMonitor:
    enabled: false      # no prom-operator in kind by default
  prometheusRule:
    enabled: false
EOF

helm install valkey ./deploy/charts/valkey-cluster \
  --namespace valkey \
  -f /tmp/values-kind.yaml \
  --wait --timeout 5m
```

**Expected:**
```
NAME: valkey
LAST DEPLOYED: ...
NAMESPACE: valkey
STATUS: deployed
REVISION: 1
NOTES:
Valkey cluster "valkey-valkey-cluster" deployed in namespace valkey.
...
```

The `--wait` flag blocks until pods are Ready **and** the post-install `CLUSTER CREATE` Job completes. If it returns successfully, the cluster is bootstrapped.

Verify the topology:
```bash
kubectl -n valkey get pods -o wide
# NAME                       READY   STATUS    NODE                    
# valkey-valkey-cluster-0    2/2     Running   valkey-test-worker       
# valkey-valkey-cluster-1    2/2     Running   valkey-test-worker2      
# valkey-valkey-cluster-2    2/2     Running   valkey-test-worker3      
# valkey-valkey-cluster-3    2/2     Running   valkey-test-worker2      
# valkey-valkey-cluster-4    2/2     Running   valkey-test-worker3      
# valkey-valkey-cluster-5    2/2     Running   valkey-test-worker       

kubectl -n valkey get pvc
# NAME                            STATUS   VOLUME    CAPACITY
# data-valkey-valkey-cluster-0    Bound    pvc-...   1Gi
# ...(6 PVCs)

kubectl -n valkey logs job/valkey-valkey-cluster-cluster-create | tail -10
# expect: "[OK] All 16384 slots covered."
```

(Pod `2/2` means valkey + metrics sidecar.)

---

## 6. Test matrix — run these in order

### T1. Helm test (built-in smoke check)

```bash
helm test valkey -n valkey
```

**Expected:**
```
NAME: valkey
LAST DEPLOYED: ...
STATUS: deployed
TEST SUITE:     valkey-valkey-cluster-connection-test
Last Started:   ...
Last Completed: ...
Phase:          Succeeded
```

```bash
kubectl -n valkey logs valkey-valkey-cluster-connection-test
# PASS: cluster_state=ok slots=16384 known=6 size=3 set/get round-trip ok
```

### T2. Cluster health

```bash
kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning cluster info
```

**Expected:**
```
cluster_state:ok
cluster_slots_assigned:16384
cluster_slots_ok:16384
cluster_known_nodes:6
cluster_size:3
```

```bash
kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning cluster nodes
```

Expect 3 lines marked `master` (each with a slot range) and 3 lines marked `slave` (each pointing at a master node ID). The `,hostname` field should show the K8s FQDN `valkey-valkey-cluster-N.valkey-valkey-cluster-headless.valkey.svc.cluster.local`.

### T3. Anti-affinity verification

```bash
kubectl -n valkey get pods -o wide --sort-by=.spec.nodeName
```

With hard anti-affinity and 3 worker nodes, you should see 2 pods per node (one master + one replica). No node should have 3 pods.

### T4. Failover via pod deletion

```bash
# Identify a master
PRIMARY_MASTER=$(kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning cluster nodes \
  | awk '/myself,master/ {print $2}' | cut -d. -f1)
echo "Killing $PRIMARY_MASTER"

# Watch nodes while the failover happens (run in a separate terminal)
# kubectl -n valkey exec valkey-valkey-cluster-1 -- watch -n2 \
#   valkey-cli -a "$VALKEY_PW" --no-auth-warning cluster info

# Trigger failover by deleting the master pod
START=$(date +%s)
kubectl -n valkey delete pod "$PRIMARY_MASTER"

# Wait for replica promotion
until [ "$(kubectl -n valkey exec valkey-valkey-cluster-1 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning cluster info \
  | awk -F: '/^cluster_state:/ {print $2}' | tr -d '\r\n')" = "ok" ]; do
  sleep 1
done
echo "cluster_state returned to OK in $(( $(date +%s) - START ))s"
```

**Expected:** cluster_state returns to OK within 30s (typically 5–15s).

```bash
# After the killed pod returns, it should rejoin as a replica of the new master
kubectl -n valkey wait --for=condition=Ready "pod/$PRIMARY_MASTER" --timeout=2m
sleep 5
kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning cluster nodes | grep -E "(master|slave)" | sort
```

Expect still 3 masters + 3 slaves. The originally-killed pod should now be a slave (the replica was promoted).

### T5. Graceful failover via preStop (rolling update test)

```bash
# Trigger a rolling update by setting an innocuous pod annotation
kubectl -n valkey patch sts valkey-valkey-cluster -p \
  '{"spec":{"template":{"metadata":{"annotations":{"rollout-test":"'"$(date +%s)"'"}}}}}'

# Watch pods cycle. Each master should run preStop before terminating.
kubectl -n valkey logs valkey-valkey-cluster-5 -c valkey --previous 2>/dev/null | tail -10
```

Look for preStop log lines like:
```
preStop: valkey-valkey-cluster-5 is a master (id=...), looking for a replica to promote
preStop: triggering CLUSTER FAILOVER on replica <fqdn>:6379
preStop: failover complete, valkey-valkey-cluster-5 is now a replica
```

Throughout the rolling update, cluster_state should stay `ok` (no write loss):
```bash
kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning cluster info
```

### T6. Persistence across pod restart

```bash
# Write data via cluster-aware client
kubectl -n valkey exec valkey-valkey-cluster-0 -- sh -c \
  "valkey-cli -c -a '$VALKEY_PW' --no-auth-warning SET t6key 'persistence-works'"

# Delete the pod that owns that key
SLOT=$(kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning CLUSTER KEYSLOT t6key)
echo "Key t6key is in slot $SLOT"

OWNER=$(kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning CLUSTER NODES \
  | awk -v slot="$SLOT" '$3 ~ /master/ {split($NF, r, "-"); split(r[1], lo, ":"); if (slot >= lo[1] && slot <= r[2]) print $2}' \
  | head -1 | cut -d. -f1)
echo "Owner pod: $OWNER"

kubectl -n valkey delete pod "$OWNER"
kubectl -n valkey wait --for=condition=Ready "pod/$OWNER" --timeout=2m
sleep 5

# Data should survive
kubectl -n valkey exec valkey-valkey-cluster-0 -- sh -c \
  "valkey-cli -c -a '$VALKEY_PW' --no-auth-warning GET t6key"
# expect: "persistence-works"
```

### T7. Scale-up (add a shard)

```bash
helm upgrade valkey ./deploy/charts/valkey-cluster \
  -n valkey \
  -f /tmp/values-kind.yaml \
  --set cluster.masters=4 \
  --wait --timeout 5m

# Watch the scale Job
kubectl -n valkey logs -l app.kubernetes.io/component=cluster-scale --tail=50
```

**Expected log:**
```
current: known_nodes=6 masters=3
desired: total=8 masters=4
scale-up: adding 1 master(s) and the corresponding replicas
adding ...:6379 as MASTER
adding ...:6379 as REPLICA of <id>
rebalancing slots across 4 masters
scale-up complete
```

Verify final topology:
```bash
kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning cluster info
# cluster_known_nodes:8
# cluster_size:4
# cluster_slots_assigned:16384

kubectl -n valkey get pods -l app.kubernetes.io/name=valkey-cluster
# expect 8 pods
```

### T8. Scale-down is correctly refused

Scaling masters back down is not auto-safe (would lose data without manual reshard first). The chart's scale Job should refuse:

```bash
helm upgrade valkey ./deploy/charts/valkey-cluster \
  -n valkey \
  -f /tmp/values-kind.yaml \
  --set cluster.masters=3 \
  --wait --timeout 2m

kubectl -n valkey logs -l app.kubernetes.io/component=cluster-scale --tail=20
```

**Expected:**
```
current: known_nodes=8 masters=4
desired: total=6 masters=3
scale-DOWN is not automated. Use the runbook at runbooks/scale-out.md (reverse procedure).
this Job will exit 0 to not block helm upgrade.
```

The StatefulSet will scale to 6 pods, but those 2 extra nodes are still in the cluster topology (just with no pods). To actually shrink, follow the manual procedure in `runbooks/scale-out.md` (reverse direction). For this test, the important thing is the chart didn't auto-delete master nodes with slots assigned.

Reset to clean state:
```bash
helm upgrade valkey ./deploy/charts/valkey-cluster \
  -n valkey \
  -f /tmp/values-kind.yaml \
  --set cluster.masters=3 \
  --wait
```

### T9. Metrics

```bash
# Port-forward metrics from one pod
kubectl -n valkey port-forward valkey-valkey-cluster-0 9121:9121 &
PF_PID=$!
sleep 2

curl -s localhost:9121/metrics | grep -E '^redis_(up|cluster_state|cluster_slots_assigned|cluster_size|memory_max_bytes) ' | head
```

**Expected:**
```
redis_up 1
redis_cluster_state 1
redis_cluster_slots_assigned 16384
redis_cluster_size 3
redis_memory_max_bytes 1.048576e+08
```

```bash
kill $PF_PID 2>/dev/null
```

### T10. NetworkPolicy enforcement

This only works if your CNI implements NetworkPolicy. kind's default CNI (`kindnet`) does **not**. Install Calico:

```bash
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml
# Wait for Calico to be ready
kubectl -n kube-system rollout status deployment/calico-kube-controllers --timeout=5m
kubectl -n kube-system rollout status daemonset/calico-node --timeout=5m
```

Then test:

```bash
# An out-of-namespace pod should NOT be able to connect
kubectl run probe-denied --image=valkey/valkey:8.1.4-alpine --rm -i --restart=Never -- \
  timeout 5 valkey-cli -h valkey-valkey-cluster-headless.valkey.svc.cluster.local \
  -p 6379 -a "$VALKEY_PW" --no-auth-warning PING
# expect: timeout / "Could not connect"
```

```bash
# A pod IN the valkey namespace SHOULD be able to connect
kubectl -n valkey run probe-allowed --image=valkey/valkey:8.1.4-alpine --rm -i --restart=Never -- \
  valkey-cli -h valkey-valkey-cluster-headless -p 6379 -a "$VALKEY_PW" --no-auth-warning PING
# expect: PONG
```

### T11. Password rotation by re-creating the Secret (no chart change)

The chart references `auth.existingSecret`, so rotating the password is a Secret operation, not a helm operation:

```bash
NEW_PW="$(openssl rand -base64 32 | tr -d /+= | head -c 32)"
kubectl -n valkey create secret generic valkey-cluster-auth \
  --from-literal=valkey-password="$NEW_PW" \
  --dry-run=client -o yaml | kubectl apply -f -

# Pods need to restart to pick up the new env var (this is K8s behavior, not
# specific to this chart). Trigger a rolling restart:
kubectl -n valkey rollout restart sts/valkey-valkey-cluster
kubectl -n valkey rollout status sts/valkey-valkey-cluster --timeout=5m

# Now the OLD password should fail
kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning PING
# expect: WRONGPASS

# And the NEW one should work
kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$NEW_PW" --no-auth-warning PING
# expect: PONG

# Update local variable
VALKEY_PW="$NEW_PW"
echo "$VALKEY_PW" > /tmp/valkey-pw.txt
```

### T12. Wrong password is rejected by helm test

Simulate a misconfiguration: deploy the test pod with a wrong password.

```bash
# (The helm test pod uses the Secret directly, so to test "wrong password"
# we render it manually with a bad value)
helm template valkey ./deploy/charts/valkey-cluster -n valkey \
  -f /tmp/values-kind.yaml \
  --set auth.existingSecret="" --set auth.password="WRONG-PASSWORD" \
  --show-only templates/tests/connection-test.yaml \
  | kubectl apply -n valkey -f -

kubectl -n valkey wait --for=condition=PodScheduled pod/valkey-valkey-cluster-connection-test --timeout=30s
kubectl -n valkey logs pod/valkey-valkey-cluster-connection-test
# expect: "AUTH failed: WRONGPASS ..." then "FAIL: cluster_state="
kubectl -n valkey delete pod valkey-valkey-cluster-connection-test
```

### T13. Pod logs and observability

```bash
# Valkey logs
kubectl -n valkey logs valkey-valkey-cluster-0 -c valkey --tail=20

# Metrics sidecar logs
kubectl -n valkey logs valkey-valkey-cluster-0 -c metrics --tail=10

# CLUSTER NODES topology
kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning cluster nodes
```

### T14. Resource limits enforced

```bash
kubectl -n valkey describe pod valkey-valkey-cluster-0 | grep -A 8 'Limits\|Requests'
```

**Expected:** memory limit 512Mi, request 256Mi.

```bash
# Container is QoS Burstable (limit > request for CPU)
kubectl -n valkey get pod valkey-valkey-cluster-0 -o jsonpath='{.status.qosClass}'
# Burstable
```

For Guaranteed QoS, set `limits` == `requests` for both cpu and memory in your values.

### T15. Long-running soak (optional, run for 1 hour)

In one terminal:
```bash
# Continuous load
kubectl -n valkey run loadgen --image=valkey/valkey:8.1.4-alpine --rm -it --restart=Never \
  -- valkey-benchmark -h valkey-valkey-cluster-headless -a "$VALKEY_PW" \
  --cluster -t set,get -n 1000000 -r 100000 -c 10
```

In another terminal, periodically kill pods:
```bash
for i in 1 2 3 4 5 6 7 8 9 10; do
  pod="valkey-valkey-cluster-$(( RANDOM % 6 ))"
  echo "[$i] killing $pod"
  kubectl -n valkey delete pod "$pod"
  sleep 60
done
```

After: confirm cluster still healthy and no data lost:
```bash
kubectl -n valkey exec valkey-valkey-cluster-0 -- \
  valkey-cli -a "$VALKEY_PW" --no-auth-warning cluster info
# expect: cluster_state:ok throughout (verify with periodic check above)
```

---

## 7. Cleanup

```bash
helm uninstall valkey -n valkey
kubectl -n valkey delete pvc --all                     # frees ~6GB local-path storage
kubectl delete namespace valkey

kind delete cluster --name valkey-test
docker volume prune -f                                  # reclaim leftover volumes
```

---

## Pass/Fail summary template

After running T1–T14, fill this in:

```
T1   helm test               [ PASS / FAIL ]    notes:
T2   cluster_state=ok        [ PASS / FAIL ]
T3   anti-affinity spread    [ PASS / FAIL ]
T4   failover on pod kill    [ PASS / FAIL ]    time-to-recover: ___s
T5   rolling update preStop  [ PASS / FAIL ]
T6   persistence/PVC         [ PASS / FAIL ]
T7   scale-up (3→4 masters)  [ PASS / FAIL ]
T8   scale-down refused      [ PASS / FAIL ]
T9   metrics /metrics        [ PASS / FAIL ]
T10  NetworkPolicy enforced  [ PASS / FAIL ]    CNI used: _____
T11  password rotation       [ PASS / FAIL ]
T12  wrong password rejected [ PASS / FAIL ]
T13  observability           [ PASS / FAIL ]
T14  resource limits         [ PASS / FAIL ]    QoS: _________
T15  soak (optional)         [ PASS / FAIL ]
```

If any FAIL: capture the pod's logs, the relevant `kubectl describe`, and
`kubectl -n valkey get events --sort-by=.lastTimestamp | tail -30`. Open a
GitHub issue with that info attached.

---

## Common failures and what to do

| Symptom | Likely cause | Fix |
|---|---|---|
| `kind create cluster` fails on cgroup mount | Host on cgroup v1 | Switch host to cgroup v2 (Linux: kernel cmdline `systemd.unified_cgroup_hierarchy=1`) |
| Image pull fails inside kind | Image not loaded; pod tries Docker Hub via NAT | Pre-load with `kind load docker-image` |
| Pods stuck `Pending` with `0/3 nodes available` | Hard anti-affinity needs 3 distinct nodes | Use 3+ worker nodes in kind config, or change `podAntiAffinityPreset: soft` |
| post-install Job fails: `timeout waiting for ...` | Pods not Ready in time | Bump `cluster.init.podReadyTimeoutSeconds` from 300 to 600, or check pod events |
| post-install Job fails: `last response: NOAUTH ...` | `existingSecret` reference mismatch | Verify the Secret exists and the password key matches `auth.existingSecretPasswordKey` |
| `helm test` fails with `FAIL: cluster_known_nodes=0` | Cluster bootstrap raced ahead of pod readiness | Re-run `helm test` after a few seconds; if persistent, see prev row |
| NetworkPolicy probe from outside still works | kind's default CNI doesn't implement NetworkPolicy | Install Calico (T10) |
| `helm uninstall` leaves PVCs | Standard K8s behavior — PVCs survive StatefulSet deletion | `kubectl delete pvc -l app.kubernetes.io/name=valkey-cluster` |

---

## What this runbook covers vs. what production needs

This runbook is a **functional acceptance test** against a local single-machine
kind cluster. It does not cover:

- Multi-AZ scheduling (kind has no zones)
- Real CSI driver behavior (kind uses `local-path-provisioner` which is simpler than EBS/GCP-PD/AzureDisk)
- Network latency under load (everything is loopback)
- Image-pull behavior against your real registry mirror
- ServiceMonitor → Prometheus Operator pickup (kind doesn't have prom-operator)
- Backup/restore against object storage
- ResourceQuota interactions in shared namespaces

If T1–T14 pass on kind, you have high confidence the chart is functionally
correct. Before prod cutover, re-run T1–T14 against your actual dev/staging
K8s cluster — the differences will be: real CSI, real CNI, real registry,
real Prometheus, real DNS — and any of those can surface environment-specific
issues that kind doesn't model.
