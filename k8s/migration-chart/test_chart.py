"""
Integration-style tests for the migration-flow-test Helm chart.

Strategy
--------
We cannot run `helm template` here, so we simulate it:
  1. Read each template file.
  2. Replace {{ .Release.Name }}, {{ .Values.* }}, and the shared helper
     includes with their test values.
  3. Split the rendered output into individual YAML documents.
  4. Parse each document and assert the structure is correct.

What is validated
-----------------
  - Every Job exists (11 total across 3 phases).
  - Phase 1 jobs carry hook-weight "10", Phase 2 carry "20", Phase 3 "30".
  - Phase 1/2 jobs inject exactly SOURCE_MONGO_URI and TARGET_MONGO_URI
    from the secret.
  - Phase 3 jobs additionally inject CASSANDRA_HOSTS, CASSANDRA_USER,
    CASSANDRA_PASSWORD.
  - All jobs read the ConfigMap via envFrom.
  - ConfigMap contains all 8 required keys.
  - Secret contains all 5 required keys.
  - restartPolicy is OnFailure for every Job.
  - hook-delete-policy is before-hook-creation for every Job.
  - backoffLimit is 2 for every Job.
"""
import re, sys, textwrap, yaml
from pathlib import Path

CHART = Path(__file__).parent
TEMPLATES = CHART / "templates"

RELEASE_NAME = "migtest"
NAMESPACE    = "migration-test"

# ── Values that would come from values.yaml ───────────────────────────────────
VALUES = {
    "namespace":          NAMESPACE,
    "image":              "busybox:1.36",
    "phase1SleepSeconds": "5",
    "phase2SleepSeconds": "8",
    "phase3SleepSeconds": "10",
    "config.siteId":               "test-site",
    "config.sourceDb":             "rocketchat",
    "config.targetDb":             "chat",
    "config.checkpointDb":         "migration_checkpoint",
    "config.batchSize":            "500",
    "config.cassandraKeyspace":    "chat",
    "config.cutoffTimestamp":      "1750000000000",
    "config.messageStartDateMs":   "1735689600000",
    "secret.sourceMongoUri":       "mongodb://source-mongo:27017/rocketchat",
    "secret.targetMongoUri":       "mongodb://target-mongo:27017/chat",
    "secret.cassandraHosts":       "cassandra-0.cassandra,cassandra-1.cassandra",
    "secret.cassandraUser":        "migration_user",
    "secret.cassandraPassword":    "test-password-not-real",
}

# ── What the _helpers.tpl includes expand to ──────────────────────────────────
# Content strings have NO leading indentation — nindent() adds the right amount.

COMMON_ENV_EXPANDED = f"""\
envFrom:
  - configMapRef:
      name: {RELEASE_NAME}-config
env:
  - name: SOURCE_MONGO_URI
    valueFrom:
      secretKeyRef:
        name: {RELEASE_NAME}-secret
        key: SOURCE_MONGO_URI
  - name: TARGET_MONGO_URI
    valueFrom:
      secretKeyRef:
        name: {RELEASE_NAME}-secret
        key: TARGET_MONGO_URI"""

# cassandraEnv items are already inside an `env:` list so they carry
# no extra 2-space leader — nindent positions them correctly.
CASSANDRA_ENV_EXPANDED = f"""\
- name: CASSANDRA_HOSTS
  valueFrom:
    secretKeyRef:
      name: {RELEASE_NAME}-secret
      key: CASSANDRA_HOSTS
- name: CASSANDRA_USER
  valueFrom:
    secretKeyRef:
      name: {RELEASE_NAME}-secret
      key: CASSANDRA_USER
- name: CASSANDRA_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {RELEASE_NAME}-secret
      key: CASSANDRA_PASSWORD"""


def _nindent(content: str, n: int) -> str:
    """Faithful simulation of Helm's nindent N: prepend \\n then indent every line."""
    pad = " " * n
    return "\n" + "\n".join(pad + line for line in content.splitlines())


def render(path: Path) -> str:
    """Simulate Helm rendering: substitute template directives with real values."""
    text = path.read_text()

    # Release / Chart references
    text = text.replace("{{ .Release.Name }}", RELEASE_NAME)
    text = text.replace("{{ .Release.Service }}", "Helm")
    text = text.replace("{{ .Chart.Name }}", "migration-flow-test")
    text = text.replace("{{ .Chart.Version }}", "0.1.0")

    # Values
    for k, v in VALUES.items():
        text = text.replace("{{ .Values." + k + " }}", v)
        text = text.replace("{{ .Values." + k + " | quote }}", f'"{v}"')

    # Helper includes — consume surrounding whitespace ({{- trims before, }} trims nothing)
    text = re.sub(
        r'\s*\{\{-?\s*include "migration\.commonEnv" \. \| nindent 8\s*-?\}\}',
        _nindent(COMMON_ENV_EXPANDED, 8),
        text,
    )
    text = re.sub(
        r'\s*\{\{-?\s*include "migration\.cassandraEnv" \. \| nindent 10\s*-?\}\}',
        _nindent(CASSANDRA_ENV_EXPANDED, 10),
        text,
    )
    label_block = (
        f"app.kubernetes.io/managed-by: Helm\n"
        f"    app.kubernetes.io/instance: {RELEASE_NAME}\n"
        f"    helm.sh/chart: migration-flow-test-0.1.0"
    )
    text = re.sub(
        r'\{\{-?\s*include "migration\.labels" \. \| nindent 4\s*-?\}\}',
        label_block,
        text,
    )

    # Warn about any unresolved {{ }} so failures are obvious
    remaining = re.findall(r'\{\{.*?\}\}', text)
    if remaining:
        print(f"  WARN unresolved directives in {path.name}: {remaining[:5]}")

    return text


def load_docs(path: Path):
    """Render and parse all YAML documents in a template file."""
    rendered = render(path)
    docs = list(yaml.safe_load_all(rendered))
    return [d for d in docs if d is not None]


# ── Helpers ───────────────────────────────────────────────────────────────────

def get_jobs(docs):
    return [d for d in docs if d.get("kind") == "Job"]

def hook_weight(job):
    return job["metadata"]["annotations"].get("helm.sh/hook-weight")

def secret_env_keys(job):
    """Return the set of env var names pulled from a secretKeyRef."""
    envs = job["spec"]["template"]["spec"]["containers"][0].get("env", [])
    return {
        e["name"]
        for e in envs
        if "valueFrom" in e and "secretKeyRef" in e["valueFrom"]
    }

def has_configmap_envfrom(job):
    envfrom = job["spec"]["template"]["spec"]["containers"][0].get("envFrom", [])
    return any(
        ef.get("configMapRef", {}).get("name") == f"{RELEASE_NAME}-config"
        for ef in envfrom
    )

def restart_policy(job):
    return job["spec"]["template"]["spec"]["restartPolicy"]

def delete_policy(job):
    return job["metadata"]["annotations"].get("helm.sh/hook-delete-policy")

# ── Test cases ────────────────────────────────────────────────────────────────

PASS = []
FAIL = []

def ok(msg):
    PASS.append(msg)
    print(f"  PASS  {msg}")

def fail(msg):
    FAIL.append(msg)
    print(f"  FAIL  {msg}")

def check(cond, msg):
    ok(msg) if cond else fail(msg)


print("\n=== ConfigMap ===")
cm_docs = load_docs(TEMPLATES / "configmap.yaml")
cms = [d for d in cm_docs if d and d.get("kind") == "ConfigMap"]
check(len(cms) == 1, "exactly 1 ConfigMap")
if cms:
    data = cms[0]["data"]
    for key in ["SITE_ID", "SOURCE_DB", "TARGET_DB", "CHECKPOINT_DB",
                "BATCH_SIZE", "CASSANDRA_KEYSPACE", "CUTOFF_TIMESTAMP",
                "MESSAGE_START_DATE_MS"]:
        check(key in data, f"ConfigMap has key {key}")


print("\n=== Secret ===")
sec_docs = load_docs(TEMPLATES / "secret.yaml")
secs = [d for d in sec_docs if d and d.get("kind") == "Secret"]
check(len(secs) == 1, "exactly 1 Secret")
if secs:
    sd = secs[0].get("stringData", {})
    for key in ["SOURCE_MONGO_URI", "TARGET_MONGO_URI",
                "CASSANDRA_HOSTS", "CASSANDRA_USER", "CASSANDRA_PASSWORD"]:
        check(key in sd, f"Secret has key {key}")


print("\n=== Phase 1 jobs (weight 10, 4 jobs) ===")
p1_docs = load_docs(TEMPLATES / "phase1-jobs.yaml")
p1_jobs = get_jobs(p1_docs)
check(len(p1_jobs) == 4, f"Phase 1: 4 Jobs (got {len(p1_jobs)})")
expected_p1 = {f"{RELEASE_NAME}-p1-users", f"{RELEASE_NAME}-p1-rooms",
               f"{RELEASE_NAME}-p1-subscriptions", f"{RELEASE_NAME}-p1-avatars"}
got_p1 = {j["metadata"]["name"] for j in p1_jobs}
check(got_p1 == expected_p1, f"Phase 1 job names correct")
for job in p1_jobs:
    n = job["metadata"]["name"]
    check(hook_weight(job) == "10",           f"{n}: hook-weight=10")
    check(restart_policy(job) == "OnFailure", f"{n}: restartPolicy=OnFailure")
    check(delete_policy(job) == "before-hook-creation", f"{n}: delete-policy")
    check(job["spec"]["backoffLimit"] == 2,   f"{n}: backoffLimit=2")
    check(has_configmap_envfrom(job),         f"{n}: envFrom ConfigMap")
    keys = secret_env_keys(job)
    check(keys == {"SOURCE_MONGO_URI", "TARGET_MONGO_URI"},
          f"{n}: secret keys = {{SOURCE_MONGO_URI, TARGET_MONGO_URI}}")


print("\n=== Phase 2 jobs (weight 20, 2 jobs) ===")
p2_docs = load_docs(TEMPLATES / "phase2-jobs.yaml")
p2_jobs = get_jobs(p2_docs)
check(len(p2_jobs) == 2, f"Phase 2: 2 Jobs (got {len(p2_jobs)})")
expected_p2 = {f"{RELEASE_NAME}-p2-room-members", f"{RELEASE_NAME}-p2-thread-rooms"}
got_p2 = {j["metadata"]["name"] for j in p2_jobs}
check(got_p2 == expected_p2, "Phase 2 job names correct")
check(f"{RELEASE_NAME}-p2-thread-subscriptions" not in got_p2,
      "Phase 2 does NOT contain thread-subscriptions (moved to Phase 3)")
for job in p2_jobs:
    n = job["metadata"]["name"]
    check(hook_weight(job) == "20",           f"{n}: hook-weight=20")
    check(restart_policy(job) == "OnFailure", f"{n}: restartPolicy=OnFailure")
    check(delete_policy(job) == "before-hook-creation", f"{n}: delete-policy")
    check(job["spec"]["backoffLimit"] == 2,   f"{n}: backoffLimit=2")
    check(has_configmap_envfrom(job),         f"{n}: envFrom ConfigMap")
    keys = secret_env_keys(job)
    check(keys == {"SOURCE_MONGO_URI", "TARGET_MONGO_URI"},
          f"{n}: secret keys = {{SOURCE_MONGO_URI, TARGET_MONGO_URI}}")


print("\n=== Phase 3 jobs (weight 30, 5 jobs) ===")
p3_docs = load_docs(TEMPLATES / "phase3-jobs.yaml")
p3_jobs = get_jobs(p3_docs)
check(len(p3_jobs) == 5, f"Phase 3: 5 Jobs (got {len(p3_jobs)})")
CASSANDRA_JOB_NAMES = {f"{RELEASE_NAME}-p3-messages-by-id", f"{RELEASE_NAME}-p3-messages-by-room",
                       f"{RELEASE_NAME}-p3-pinned-messages", f"{RELEASE_NAME}-p3-thread-messages"}
expected_p3 = CASSANDRA_JOB_NAMES | {f"{RELEASE_NAME}-p3-thread-subscriptions"}
got_p3 = {j["metadata"]["name"] for j in p3_jobs}
check(got_p3 == expected_p3, "Phase 3 job names correct (4 Cassandra + thread-subscriptions)")

CASSANDRA_KEYS = {"SOURCE_MONGO_URI", "TARGET_MONGO_URI",
                  "CASSANDRA_HOSTS", "CASSANDRA_USER", "CASSANDRA_PASSWORD"}
MONGO_KEYS     = {"SOURCE_MONGO_URI", "TARGET_MONGO_URI"}

for job in p3_jobs:
    n = job["metadata"]["name"]
    check(hook_weight(job) == "30",           f"{n}: hook-weight=30")
    check(restart_policy(job) == "OnFailure", f"{n}: restartPolicy=OnFailure")
    check(delete_policy(job) == "before-hook-creation", f"{n}: delete-policy")
    check(job["spec"]["backoffLimit"] == 2,   f"{n}: backoffLimit=2")
    check(has_configmap_envfrom(job),         f"{n}: envFrom ConfigMap")
    keys = secret_env_keys(job)
    if n == f"{RELEASE_NAME}-p3-thread-subscriptions":
        check(keys == MONGO_KEYS,
              f"{n}: secret keys = {{SOURCE_MONGO_URI, TARGET_MONGO_URI}} (MongoDB only)")
    else:
        check(keys == CASSANDRA_KEYS,
              f"{n}: secret keys include all 5 Cassandra vars")


print("\n=== Phase ordering invariant ===")
# Weights must be strictly ascending: Phase 1 < Phase 2 < Phase 3
w1 = {hook_weight(j) for j in p1_jobs}
w2 = {hook_weight(j) for j in p2_jobs}
w3 = {hook_weight(j) for j in p3_jobs}
check(w1 == {"10"} and w2 == {"20"} and w3 == {"30"},
      "Phase 1 weight(10) < Phase 2 weight(20) < Phase 3 weight(30)")
check(max(int(w) for w in w1) < min(int(w) for w in w2),
      "max(Phase 1 weight) < min(Phase 2 weight)")
check(max(int(w) for w in w2) < min(int(w) for w in w3),
      "max(Phase 2 weight) < min(Phase 3 weight)")


print("\n=== Namespace ===")
ns_docs = load_docs(TEMPLATES / "namespace.yaml")
nss = [d for d in ns_docs if d and d.get("kind") == "Namespace"]
check(len(nss) == 1, "exactly 1 Namespace")
if nss:
    check(nss[0]["metadata"]["name"] == NAMESPACE, f"Namespace name = {NAMESPACE}")


# ── Summary ───────────────────────────────────────────────────────────────────
total = len(PASS) + len(FAIL)
print(f"\n{'='*50}")
print(f"Results: {len(PASS)}/{total} passed, {len(FAIL)} failed")
if FAIL:
    print("Failed checks:")
    for f in FAIL:
        print(f"  - {f}")
    sys.exit(1)
else:
    print("All checks passed.")
