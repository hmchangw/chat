# Runbook — Encrypted-search harnesses (quality + performance)

Operational runbook for the two encrypted-message-search benchmark harnesses
delivered in Phase 2 of the encrypted-message-search work:

- **Quality harness** (`tools/searchquality`) — offline, deterministic, single
  Elasticsearch. Answers *"how much search quality does encryption cost?"*
- **Performance / loadgen harness** (`tools/loadgen search-sustained`) — load
  test against a live services stack. Answers *"A vs B vs C query latency?"*

Design references: `docs/superpowers/specs/2026-06-03-encrypted-message-search-design.md`
§8 (the two harnesses) and §15 (the empirically-confirmed CJK / HTML parity
findings that change how the quality gate is read).

The companion artifact `tools/searchquality/sample-report.md` is a **real**
quality report produced by the run documented below.

---

## 1. Quality harness (`tools/searchquality`)

### 1.1 What it does

It indexes a small multilingual corpus
(`tools/searchquality/testdata/corpus.json`) into two Elasticsearch indices:

- **C** — today's plaintext path (the `custom_analyzer` index).
- **blind** — the encrypted path (Go `pkg/msganalyzer` tokens HMAC'd via
  `pkg/blindsearch` into the `contentBlind` field). Arms A and B share the same
  blind hits, so a single blind index represents both.

It runs a per-language query set against both indices and reports, per language:

| Metric | Meaning |
|---|---|
| **Recall@10** | Of the docs known to be relevant for a query, how many the **blind** index still surfaces in its top 10. Measured against a ground-truth relevance set, *not* against C. |
| **Jaccard** | Set overlap of the top-10 `_id` lists, **C vs blind**. |
| **RBO** | Rank-biased overlap of the full ranked `_id` lists, **C vs blind**, at persistence `p=0.90`. A rank-correlation: 1.0 = identical ranking, 0.0 = disjoint. |
| **Parity divergences** | Count of corpus docs whose Go `msganalyzer.Analyze` token stream differed from ES `_analyze` on the same input. **Diagnostic only — does not fail the gate.** |

### 1.2 How to run it

Only Elasticsearch is required (no Mongo / Cassandra / NATS / Vault). Bring up a
standalone ES from the cached image and point the CLI at it. This is exactly the
command that produced the committed `sample-report.md`:

```bash
# 1. Standalone single-node ES on a fixed port.
docker run -d --name sq-es -p 9209:9200 \
  -e discovery.type=single-node \
  -e xpack.security.enabled=false \
  -e ES_JAVA_OPTS="-Xms512m -Xmx512m" \
  elasticsearch:8.17.0

# 2. Wait for cluster health (can take 30-60s under the VFS storage driver).
until curl -s http://localhost:9209/_cluster/health | grep -qE '"status":"(yellow|green)"'; do sleep 3; done

# 3. (VFS / low-disk hosts only) If the data disk is >85% full, ES parks new
#    primaries unassigned (disk low-watermark) and the bulk index fails with
#    "primary shard is not active". Disable the disk allocation decider:
curl -s -XPUT http://localhost:9209/_cluster/settings \
  -H 'Content-Type: application/json' \
  -d '{"transient":{"cluster.routing.allocation.disk.threshold_enabled":false}}'

# 4. Run the harness; it writes the markdown report to -out and prints it.
go run ./tools/searchquality \
  -es-url=http://localhost:9209 \
  -corpus=tools/searchquality/testdata/corpus.json \
  -out=tools/searchquality/sample-report.md

# 5. Clean up.
docker rm -f sq-es
```

CLI flags (defaults in parentheses) — see `tools/searchquality/main.go`:

| Flag | Default | Purpose |
|---|---|---|
| `-es-url` | `http://localhost:9200` | Elasticsearch URL |
| `-corpus` | `testdata/corpus.json` | corpus path |
| `-out` | `report.md` | output markdown report path |
| `-blind-key` | `ab`×32 | hex blind-index key (32 bytes / 64 hex chars) |
| `-blind-key-version` | `v1` | blind-index key version |
| `-k` | `10` | top-K cutoff for recall/Jaccard |
| `-p` | `0.9` | RBO persistence parameter |
| `-recall-gate` | `0.95` | minimum mean recall@K per language |
| `-rbo-gate` | `0.90` | minimum mean RBO per language |

The process exits 0 even when the printed gate verdict is FAIL; the verdict is
in the report body (`OverallPassed`). Read the per-language table, do not gate
CI purely on the printed "FAIL" string — see the CJK caveat below.

### 1.3 Reading the gate

Per the spec §8a acceptance gate, a language **passes** when:

> **mean recall@10 ≥ 0.95 AND mean RBO ≥ 0.90**

applied per language so a regression in one language can't hide behind another.
Parity divergences are reported but never fail the gate (they're a diagnostic
oracle).

### 1.4 The CJK caveat (spec §15) — read this before reacting to a FAIL

**The CJK row will show `recall@10 = 1.000` but `RBO = 0.000` / `Jaccard = 0.000`,
and the overall verdict will print FAIL. This is expected and documented, not a
bug.**

Why: ES's production `custom_analyzer` does **not** bigram CJK. Its
`cjk_bigram` token filter only fires on CJK-*typed* tokens (from
`standard`/`icu` tokenizers), but the analyzer's `underscore_preserving`
**pattern** tokenizer emits `word`-typed tokens, so `cjk_bigram` is a no-op —
`公园散步` stays one whole-run token in C. The Go analyzer **does** bigram CJK
(`公园, 园散, 散步`), so the blind index can do CJK substring matching that the old
plaintext index never could.

Consequently, for CJK, **RBO/Jaccard "vs C" under-credit the blind path** —
RBO≈0 reflects C's weakness, not a blind-index defect. The harness confirms
**CJK recall@10 = 1.0 against the ground-truth relevance set**, which is the
metric to trust for CJK. Because cutover *replaces* C with the blind index, this
CJK improvement is what ships; no action is needed beyond reading the gate
correctly.

The HTML/mixed parity-divergence lines on the bare `&` token (ES `html_strip`
keeps `&`, Go drops it) are likewise benign (§15.2) — nobody searches a bare
`&`; those languages still PASS on recall/RBO.

### 1.5 The committed sample report

`tools/searchquality/sample-report.md` was produced by the run above against
`elasticsearch:8.17.0`. Real per-language numbers:

| Language | Queries | Recall@10 | Jaccard | RBO | Parity divergences | Gate |
|---|---|---|---|---|---|---|
| cjk | 4 | 1.000 | 0.000 | 0.000 | 5 | FAIL (CJK caveat — see §1.4) |
| english | 4 | 1.000 | 1.000 | 1.000 | 0 | PASS |
| html | 3 | 1.000 | 1.000 | 1.000 | 2 | PASS |
| mixed | 3 | 1.000 | 1.000 | 1.000 | 2 | PASS |

Interpretation: English/HTML/mixed pass cleanly. CJK is the documented §15
divergence — **recall@10 = 1.0 confirms the blind index finds every relevant CJK
doc**; the 0.0 RBO/Jaccard is C's CJK weakness, not a blind defect. The harness
overall-verdict prints FAIL solely because of that CJK RBO-vs-C row; per the
spec, CJK is judged on recall@10, so the substantive quality bar is met.

---

## 2. Performance / loadgen harness (`tools/loadgen search-sustained`)

> **Status: partially exercised in the build sandbox; full A/B/C benchmark
> deferred to a real stack.** This harness needs the full services stack
> (`search-service` + `search-sync-worker` + Mongo + Cassandra + NATS +
> Vault/atrest). Update (2026-06-03 re-run): `mongo:8.2.9` and `cassandra:5`
> **are now pullable** and were brought up **healthy** alongside NATS, Valkey,
> and Elasticsearch — the entire data tier stands up, and both
> `search-service` and `search-sync-worker` **build cleanly** (`make build`). The
> remaining blocker is **not** image availability but the sandbox's VFS disk
> ceiling: with no copy-on-write, the data tier already sits at ~98% disk, leaving
> no headroom to also stand up the service images plus the enc-path
> Vault/auth-callout/seed pipeline without tripping Elasticsearch's disk
> low-watermark (see §3). The A/B/C latency comparison is therefore still a
> **real-stack** activity; the commands below are ready to run there.

### 2.1 What it does

A NATS `search-sustained` workload drives `search-service` query RPCs at a fixed
rate for each arm and records latency into `loadgen_search_latency_seconds{arm}`:

- **C** — plaintext control (today's `custom_analyzer` index).
- **A** — blind query + ES-side decrypt of `contentEnc`/`encNonce` from `_source`.
- **B** — blind query + Cassandra/history-service fetch by `_id`.

It reuses the existing loadgen seeding, sharded collector, Prometheus/Grafana,
and CSV export. Arm selection requires `search-service` to be in **bench mode**
so it honors the per-request `variant`.

### 2.2 Bring up the stack with encryption + bench mode

From the repo root:

```bash
# Deps (NATS + ES + Mongo + Cassandra + Valkey + Vault).
docker compose -f docker-local/compose.deps.yaml up -d

# A test blind-index key: 32 bytes as 64 hex chars. NOT a production key.
export BLINDIDX_KEY=$(python3 -c "print('ab'*32)")   # or: openssl rand -hex 32
export BLINDIDX_KEY_VERSION=v1
```

Then start the two services with the encrypted path + bench mode enabled. The
env-var names are absolute for the blind key and `SEARCH_`-prefixed for the enc
knobs (see `search-service/main.go` `EncConfig`/`BlindConfig` and
`search-sync-worker/main.go`):

`search-sync-worker` (writes the parallel encrypted index):

```
ENC_ENABLED=true
BLINDIDX_KEY=<hex>
BLINDIDX_KEY_VERSION=v1
ENC_MSG_INDEX_PREFIX=enc-messages-site-local-v1   # MUST end -v<N>, base != MSG_INDEX_PREFIX base
MSG_INDEX_PREFIX=messages-site-local-v1
```

`search-service` (gains the flag-gated A/B query path; bench mode honors the
per-request `variant` arm):

```
SEARCH_ENC_ENABLED=true
SEARCH_BENCH_MODE_ENABLED=true
SEARCH_ENC_DEFAULT_ARM=C                # baseline arm when no per-request variant
SEARCH_ENC_MSG_INDEX_PREFIX=enc-messages-site-local-v1
BLINDIDX_KEY=<hex>
BLINDIDX_KEY_VERSION=v1
```

Bring the services up via the local compose stack (add the env above to the
service compose entries / an override file — the committed
`search-service/deploy/docker-compose.yml` and
`search-sync-worker/deploy/docker-compose.yml` do not set the enc vars):

```bash
docker compose -f docker-local/compose.services.yaml up -d search-service search-sync-worker
```

Seed a corpus so there is something to search (history/messages fixtures). The
`search-sustained` command builds its own in-memory account/room/subscription
fixtures from the matching `history-*` preset, then publishes search RPCs; the
indexed messages come from your seeded stack (e.g. `loadgen history-*` seeding or
a normal message run feeding `search-sync-worker`).

> **Seed content is searchable by construction.** The `history-*` seeder draws
> message bodies from a deterministic multilingual vocabulary
> (`tools/loadgen/searchcontent.go`) that **embeds every term in the
> `search-sustained` query pool** (English single tokens + phrases and CJK terms).
> Earlier the seeder emitted random alphanumeric filler, so every benchmark query
> matched **zero** documents and arms A/B never exercised the result-decrypt path
> — the latency numbers would have been meaningless. With the vocabulary seeder,
> a seeded corpus is guaranteed to contain documents each query pool term hits, so
> the measured latency reflects real scoring + (for A/B) decrypt work. Coverage is
> enforced by `TestSearchableContent_CoversQueryPool`.

### 2.3 Run one arm at a time

`search-sustained` flags (see `tools/loadgen/search_main.go`):

| Flag | Default | Purpose |
|---|---|---|
| `--preset` | (required) | `search-small` \| `search-medium` \| `search-large` |
| `--arm` | (required) | `C` \| `A` \| `B` (case-insensitive) |
| `--seed` | `42` | RNG seed |
| `--duration` | preset default | run duration (`0` = preset) |
| `--rate` | preset default | target req/sec (`0` = preset) |
| `--warmup` | `10s` | warmup window (samples discarded) |
| `--request-timeout` | `5s` | per-request timeout |
| `--csv` | "" | optional CSV output path |

Run each arm in turn (the loadgen container reaches `search-service` over NATS):

```bash
# Inside the loadgen container (or a built ./loadgen binary with NATS_URL set):
loadgen search-sustained --preset search-small --arm C --csv /results/search-C.csv
loadgen search-sustained --preset search-small --arm A --csv /results/search-A.csv
loadgen search-sustained --preset search-small --arm B --csv /results/search-B.csv
```

Each run prints a per-arm `p50/p95/p99/max` summary table for the measured
(post-warmup) window and, with `--csv`, one row per latency sample tagged by arm.

To keep VFS runs tiny (the goal is "the harness runs and emits a report," not a
real benchmark), override the preset:

```bash
loadgen search-sustained --preset search-small --duration 20s --rate 2 --arm C --csv /results/search-C.csv
```

### 2.4 Read the latency in Grafana

Bring up the loadgen Prometheus + Grafana dashboards and run with the dashboards
profile:

```bash
make -C tools/loadgen/deploy run-dashboards PRESET=search-small
```

Open Grafana and the **"Search latency by arm"** panel, which plots
`loadgen_search_latency_seconds{arm="C|A|B"}` (histogram quantiles per arm). Run
each arm and compare p50/p95/p99 across C/A/B; the Phase-2 recommendation
(A-vs-B) is decided on this latency plus per-arm ES index size
(`GET _cat/indices?v` filtered to `messages-*` vs `enc-messages-*`).

---

## 3. Backfill / cutover migration — populate the encrypted index for historical messages

This is the operational procedure for Phase 3 cutover (spec §9, §12): seed the
parallel encrypted index from historical `MESSAGES_CANONICAL` traffic, verify it
against the plaintext index, then flip the read path and (optionally) stop
plaintext writes.

The mechanism is the **existing `SYNC_MESSAGES_FROM` cutover filter** reused for
the enc path. In `search-sync-worker/messages.go`, `BuildAction` applies the
`syncFrom` cutoff (a message whose `Message.CreatedAt` predates the cutoff emits
**zero** actions) *before* it builds either the plaintext or the encrypted
action. So replaying `MESSAGES_CANONICAL` with `ENC_ENABLED=true` and
`SYNC_MESSAGES_FROM=<cutover ts>` populates the encrypted index for exactly
`createdAt >= SYNC_MESSAGES_FROM` and never writes pre-cutoff docs into it.
(Gating proven by `TestMessageCollection_BuildAction_SyncFromFilter_GatesEnc`.)

### 3.1 Known limitation — JetStream retention bounds the replay (spec §12)

**Only messages still resident on the `MESSAGES_CANONICAL` stream can be
replayed.** The durable consumer reads from the JetStream stream; once a message
has aged out of `MESSAGES_CANONICAL` (stream retention / max-age / max-bytes), it
is gone from the replay source. Backfill via `SYNC_MESSAGES_FROM` therefore
covers history back to **whichever is later**: the stream's retention horizon or
your chosen cutover timestamp. Older history (predating the stream retention
window) needs a **separate reindex source** that re-publishes canonical events
from durable storage (Cassandra `messages_by_room`) into `MESSAGES_CANONICAL`, or
a direct bulk-index job. That reindex source is **not** built — it is the known
gap flagged in spec §12 ("Reindex on cutover"). Pick the cutover timestamp with
this in mind: if it is older than the stream retention horizon, the window
between them will be silently absent from the enc index until a reindex source
exists.

### 3.2 Procedure (ordered)

1. **Choose the cutover timestamp.** `SYNC_MESSAGES_FROM=<RFC3339 / parseable ts>`
   — the oldest `createdAt` you want in the encrypted index. Confirm it is **at
   or after** the `MESSAGES_CANONICAL` retention horizon (see §3.1); anything
   older won't replay.

2. **Enable the enc dual-write on `search-sync-worker`** with the cutoff set (env
   per §2.2):

   ```
   ENC_ENABLED=true
   SYNC_MESSAGES_FROM=<cutover ts>
   BLINDIDX_KEY=<hex>
   BLINDIDX_KEY_VERSION=v1
   ENC_MSG_INDEX_PREFIX=enc-messages-site-local-v1
   MSG_INDEX_PREFIX=messages-site-local-v1
   PLAINTEXT_INDEX_ENABLED=true     # keep plaintext live during backfill+verify
   ```

   Leave `PLAINTEXT_INDEX_ENABLED=true` for now — the plaintext index is your
   verification baseline and the live read path until the flip in step 6.

3. **Replay the durable consumer from the cutover point.** Reset the
   `message-sync` durable consumer so it re-delivers `MESSAGES_CANONICAL` from the
   start of the retained stream (or from a sequence/time ≥ the cutover). The
   `syncFrom` filter drops anything older than the cutoff, so the consumer can
   start at the stream's first retained message safely. For example, recreate the
   durable with `DeliverPolicy=All` (or `ByStartTime` at the cutover ts), then let
   it drain. New live traffic continues to flow through the same consumer, so the
   enc index stays current after the backfill completes — backfill and steady
   state use one path.

4. **Let the backfill drain.** Monitor consumer lag (NATS `nats consumer info`
   for `MESSAGES_CANONICAL` / `message-sync`: `Unprocessed`/`Pending` → 0) and the
   worker's index error metrics. Cipher/Vault failures NAK and redeliver (they are
   transient by design), so a transient Vault blip does not lose docs — it retries.

5. **Verify enc-index doc counts vs plaintext.** Compare the two indices for the
   backfilled window. Force a refresh first, then count both, scoped to the same
   `createdAt >= SYNC_MESSAGES_FROM` range:

   ```
   POST messages-site-local-v1-*/_refresh
   POST enc-messages-site-local-v1-*/_refresh

   # Plaintext count for the backfilled window:
   GET messages-site-local-v1-*/_count
   { "query": { "range": { "createdAt": { "gte": "<cutover ts>" } } } }

   # Encrypted-index count for the same window:
   GET enc-messages-site-local-v1-*/_count
   { "query": { "range": { "createdAt": { "gte": "<cutover ts>" } } } }
   ```

   The two counts should match for `createdAt >= SYNC_MESSAGES_FROM`. A shortfall
   on the enc side means undelivered/NAK'd docs — re-check consumer lag (step 4)
   and Vault availability before proceeding. Spot-check a few known messages via
   the §4 "encrypted-yet-searchable" blind-token query to confirm content
   is actually searchable, not just present.

6. **Flip the read path (post-verification only).** Once counts match and
   spot-checks pass, point `search-service` at the encrypted index by default:

   ```
   SEARCH_ENC_DEFAULT_ARM=A      # or B — the arm chosen by the Phase-2 benchmark
   ```

   (`SEARCH_ENC_DEFAULT_ARM=C` is the plaintext baseline; A/B read the encrypted
   index.) This is the actual cutover — search queries now resolve against
   `contentBlind` and decrypt from `contentEnc` (A) or history-service (B).

7. **(Optional) Stop plaintext writes.** After the read path is on the enc index
   and stable, set on `search-sync-worker`:

   ```
   PLAINTEXT_INDEX_ENABLED=false
   ```

   `BuildAction` then emits only the enc action; the plaintext template is no
   longer upserted (its `TemplateBody` returns nil). At least one of
   `PLAINTEXT_INDEX_ENABLED` / `ENC_ENABLED` must stay true — `main` validates
   this at startup so a message never produces zero index actions. The now-stale
   plaintext indices can be deleted on your own schedule once you are confident in
   the rollback window.

**Rollback:** before step 7, rollback is trivial — set `SEARCH_ENC_DEFAULT_ARM=C`
to return to the plaintext read path (the plaintext index is still being written
while `PLAINTEXT_INDEX_ENABLED=true`). After step 7, rollback requires
re-enabling plaintext writes and re-backfilling the gap, so only disable plaintext
once the enc path is proven.

---

## 4. Kibana / ES demo — "encrypted yet searchable"

The proof that the encrypted index stores no readable message text yet still
returns the right document for a content query. Against the stack from §2 (enc
enabled, so `search-sync-worker` is writing `enc-messages-site-local-v1-*`):

**3a. Show a stored doc is opaque.** In Kibana Dev Tools (or curl), fetch any
encrypted-index doc and inspect its `_source`:

```
GET enc-messages-site-local-v1-*/_search
{ "size": 1, "query": { "match_all": {} } }
```

In the result `_source`:

- **`contentBlind`** — a whitespace-separated list of HMAC hashes (fixed-width
  hex/base tokens), **not** the message words. This is the only searchable
  representation of the content.
- **`contentEnc`** — a base64 blob (ES `binary` mapping): the AES-GCM
  ciphertext of the message. No readable plaintext.
- **`encNonce`** — a base64 blob (ES `binary` mapping): the per-message nonce.
- There is **no `content` field** on the encrypted index (the plaintext
  `custom_analyzer` field is intentionally absent — spec §6).

So an operator with raw ES/Kibana access sees only hashes and ciphertext.

**3b. Show it's still searchable.** A normal content search through
`search-service` (which HMACs the query terms the same way via `pkg/blindsearch`
and matches against `contentBlind`) returns that exact document — proving the
index is searchable without ever storing readable text. The match happens on
`contentBlind`; content is decrypted only at the end for authorized clients
(arm A from `contentEnc`, arm B from history-service).

To demonstrate the blind match directly in ES, take a known word from a seeded
message, HMAC it with `BLINDIDX_KEY` (the same derivation `pkg/blindsearch` uses)
to get its blind token, then:

```
GET enc-messages-site-local-v1-*/_search
{ "query": { "match": { "contentBlind": "<blind-token-hash>" } } }
```

It returns the doc whose opaque `contentBlind` contains that hash — encrypted at
rest, searchable in motion.

---

## 5. What was actually run in the sandbox vs deferred to your stack

| Item | Status |
|---|---|
| **Quality harness** (`tools/searchquality`) against real `elasticsearch:8.17.0` | **RAN.** Produced the committed `tools/searchquality/sample-report.md`. English/HTML/mixed PASS; CJK shows recall@10 = 1.0 with RBO/Jaccard = 0 (documented §15 CJK divergence, not a defect). |
| **Performance harness** (`loadgen search-sustained`, arms C/A/B) | **PARTIALLY exercised; full A/B/C benchmark DEFERRED to your stack.** 2026-06-03 re-run: `mongo:8.2.9` + `cassandra:5` now pull and run **healthy** (with NATS + Valkey + ES the full data tier stands up), and `search-service` / `search-sync-worker` **build cleanly** on the host. The remaining blocker is the VFS disk ceiling (~98% with the data tier alone — no room for the service images + Vault/auth/seed pipeline without tripping the ES disk watermark), **not** image availability. Commands in §2 are ready to run on a real stack. |
| **Load-test seed content** (`tools/loadgen/searchcontent.go`) | **FIXED + unit-tested.** Seeded message bodies now come from a deterministic multilingual vocabulary embedding the full query pool, so benchmark searches return real hits (arms A/B exercise decrypt) instead of zero-result latency. Previously random alphanumeric filler. |
| **Kibana / ES "encrypted-yet-searchable" demo** | **DEFERRED to your stack** (needs the enc index from a live `search-sync-worker`). Procedure documented in §3. |

The quality harness needs only Elasticsearch, which is why it produced a real
report here; the latency and Kibana demos need the full stack and are documented
for the operator to run on their own environment.
