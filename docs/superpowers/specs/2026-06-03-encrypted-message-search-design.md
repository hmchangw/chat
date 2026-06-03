# Encrypted Message Search in Elasticsearch — Design

**Date:** 2026-06-03
**Status:** Approved (brainstorming complete; pending spec review)
**Scope:** Message content only. The `spotlight` (room-name) and `user-room` (access-control) indices are out of scope.

## 1. Problem

Message content is encrypted at rest in Cassandra (`pkg/atrest`, AES-256-GCM, per-room DEK) so nobody can read it from the database. But the same content is indexed **in plaintext** in Elasticsearch (`search-sync-worker` writes a `content` field analyzed by `custom_analyzer`). Anyone with Kibana or direct ES access can read every message. We want ES to hold message content in a form that is **not human-readable**, while preserving the search functionality of the existing message index template.

## 2. Why naive encryption does not work

AES-GCM (what Cassandra uses) is randomized and semantically secure — you cannot search over it at all. Server-side full-text search over encrypted content requires **blind indexing** (searchable symmetric encryption): tokenize the content, then store a **deterministic keyed hash** of each token. ES matches hashes against hashes; it never sees plaintext.

This forces two design consequences that drive the whole spec:

- The analyzer must run **in Go, at index time and query time**, because ES can no longer analyze ciphertext. We port the analyzer and **drop the ES `custom_analyzer`** from the message index. Once we own both sides, we need the two sides to be *self-consistent*, not bug-for-bug identical to Lucene; parity with the old analyzer matters only as a search-quality baseline (see §7).
- The token hash must be **deterministic and stable**, so query tokens match indexed tokens.

## 3. Decisions (locked during brainstorming)

| # | Decision | Rationale |
|---|----------|-----------|
| D1 | **Blind indexing** (HMAC-SHA256 over analyzer tokens), not field-level AES for search | Only technique that supports server-side full-text search over non-readable content. |
| D2 | **Threat model: "cannot read plaintext."** Statistical token-frequency/equality leakage is an accepted tradeoff. | Defeats casual insider reading + Kibana/snapshot exposure (the real threat). True zero-leakage is incompatible with server-side search. |
| D3 | **Match features: whole-word + phrase + CJK only.** Prefix / as-you-type on message search is dropped. | Whole-word + phrase + BM25 are cheap to preserve. Prefix needs edge-ngram blind tokens → bigger index + more leakage. Typeahead lives in the out-of-scope `spotlight` index. |
| D4 | **Two keys, two jobs.** Searchability uses a **single global (federation-wide) HMAC blind key**. Confidentiality of the displayed text uses the **per-room `atrest` DEK** (reused, unchanged). | Message search is cross-cluster (`messages-*` + `*:messages-*`) — one query body fans across all federated clusters. Only a shared key gives **1 hash per query term**; per-site/per-room keys reintroduce ×#sites/×#rooms fan-out. The global key only ever touches *search tokens*; a compromise leaks statistical patterns, never a readable message. |
| D5 | **Defer live key rotation.** Add a `blindKeyVersion` field now; do not build rotation machinery. | Blind-index rotation requires a full reindex (matching needs all docs on the current key) — Cassandra's grace-period rotation is meaningless here. YAGNI: design the seam, defer the engine. |
| D6 | **A-vs-B content retrieval is decided by benchmark**, latency-weighted → **A is the front-runner**. | User priority is search p95/p99. A = one ES round-trip; B = +Cassandra hop. Both prototyped; benchmark quantifies the delta and A's ES-storage cost. |

## 4. Architecture

Two keys with strict separation:

- **Content key (confidentiality):** per-room `atrest` DEK. The content blob stored in ES (Approach A) is the **exact same `enc_payload` + `nonce`** Cassandra stores. Decryption reuses `pkg/atrest`. Semantically secure, zero leakage. The global key never touches it.
- **Blind key (searchability):** one global HMAC-SHA256 key, provisioned via Vault, distributed to every site's `search-sync-worker` and `search-service`, loaded once at startup and cached. Used only to hash analyzer tokens.

```
                       MESSAGES_CANONICAL (plaintext content)
                                  │
                     ┌────────────┴─────────────┐
                     ▼                           ▼
              message-worker              search-sync-worker
            (atrest encrypt →           (pkg/msganalyzer → tokens
             Cassandra enc_payload)      → pkg/blindidx HMAC(globalKey)
                                          → contentBlind text field)
                                         (Approach A also: atrest encrypt
                                          → contentEnc + encNonce)
                                                   │
                                                   ▼
                                          ES messages-* index
                                          (blind tokens + metadata
                                           + optional enc blob)
                                                   │
                                                   ▼
                                            search-service
                              (pkg/msganalyzer + pkg/blindidx on the query
                               → match contentBlind; A: decrypt enc blob
                               via atrest; B: fetch+decrypt via history-service)
```

## 5. New shared packages

### `pkg/msganalyzer`
Go port of the current ES analyzer pipeline, applied at both index and query time:
1. `html_strip` char filter (strip tags/entities before tokenizing).
2. Pattern tokenizer: split on `[\s,;!?()\[\]{}"'<>]+`, **preserving underscores**.
3. `word_delimiter_graph` semantics: `split_on_case_change=false`, `split_on_numerics=false`, `preserve_original=true`.
4. `cjk_bigram` (highest parity risk — Chinese recall depends on it).
5. `lowercase`.

Returns an **ordered** `[]string` of tokens (order retained so phrase matching survives). Pure, dependency-free, table-test friendly. Parity oracle: ES `_analyze` (see §7).

### `pkg/blindidx`
- `BlindTokens(tokens []string) []string` → `HMAC-SHA256(globalKey, token)` truncated to 16 bytes, hex/base62-encoded.
- Key separation enforced: distinct Vault key + distinct purpose label; never the `atrest` DEK.
- Carries the active `KeyVersion` for D5.

## 6. ES document & index changes (encrypted message index)

Replace the `content` field; keep **every** metadata field byte-for-byte (`messageId`, `roomId`, `siteId`, `userId`, `userAccount`, `createdAt`, `editedAt`, `updatedAt`, `threadParent*`, `tshow`). These are metadata, not content — Cassandra leaves them plaintext too.

| Field | Type | Notes |
|-------|------|-------|
| `contentBlind` | `text`, `whitespace` analyzer | Space-joined ordered blind tokens. `whitespace` analyzer keeps **positions** (phrase) and **term frequencies** (BM25). No `custom_analyzer`. |
| `contentEnc` | `binary`, `index:false` | **Approach A only.** Same `enc_payload` as Cassandra. Not analyzed/searched. |
| `encNonce` | `keyword`, `index:false` | **Approach A only.** atrest nonce for the blob. |
| `blindKeyVersion` | `keyword` | D5 rotation seam. |

The ES `custom_analyzer` and the plaintext `content` field are removed from the encrypted index template.

## 7. `search-service` query path

1. Run `msganalyzer` on the raw query → ordered tokens.
2. `blindidx.BlindTokens` → hashed terms.
3. Build the match over `contentBlind`:
   - default: `match` with `operator: AND` (whole-word AND, BM25-ranked);
   - phrase queries: `match_phrase` (positions preserved).
   - **No** `bool_prefix` (D3).
4. Access-control filter clauses (`roomId` terms-lookup, restricted-room time gates) are **unchanged** — they operate on plaintext metadata.
5. Result content:
   - **A:** read `contentEnc`/`encNonce` from `_source`, decrypt via `atrest` (per-room DEK).
   - **B:** collect `_id`s, call `history-service.GetMessagesByIDs` (existing decrypting path).

Cross-cluster behavior is unaffected: the single hashed query body matches across all clusters because the blind key is global (D4).

## 8. Benchmark / analysis harnesses

Two harnesses, because there are two distinct questions.

### 8a. Quality harness (offline, deterministic) — "how much search quality does encryption cost?"
- Seed a **large multilingual corpus** (tens of thousands of docs) into two indices: **C** (today's plaintext `custom_analyzer`) and **blind** (A/B share identical hits).
- Corpus dimensions (each with its own query set **and its own acceptance gate**, so a regression in one language can't hide behind another):
  - **English** — word boundaries, underscores/identifiers (`foo_bar`, `camelCase`), punctuation, stopword-heavy text.
  - **Chinese / CJK** — pure CJK + mixed CJK/ASCII; the `cjk_bigram` parity hot-spot.
  - **HTML** — tag/entity-laden bodies; assert tokens never contain markup (`html_strip` parity).
  - **Mixed** — English + Chinese + HTML in one message.
- Metrics vs C: **recall@K**, **top-K Jaccard overlap**, **RBO / Kendall-τ** rank correlation.
- **Parity oracle:** every `msganalyzer` token stream is cross-checked against ES `_analyze` on the same input; divergences are traced to specific analyzer rules and fixed.
- **Acceptance gate (per language, tunable):** recall@10 ≥ 0.95 **and** RBO ≥ 0.90 vs C.

### 8b. Performance harness (load) — "A vs B vs C latency + index size"
- New **`search` workload** in `tools/loadgen`, built on the existing `rpsWorkload` / `max-rps` machinery; new `search-sustained` subcommand and `search-small|medium|large` presets.
- **Arms:** **C** (plaintext control), **A** (blind + ES-decrypt), **B** (blind + Cassandra-fetch).
- **Metrics:** query `p50/p95/p99` per arm (`loadgen_search_latency_seconds{arm}`), `sync-worker` indexing throughput, **per-arm ES index size** (`_cat/indices`), analyzer+HMAC CPU.
- Reuses loadgen seeding, sharded collector, Prometheus/Grafana, CSV export.

## 9. Phasing (sequenced; non-destructive until cutover)

| Phase | Deliverable | Gate |
|-------|-------------|------|
| **0** | `pkg/msganalyzer` + `pkg/blindidx` (TDD; parity tests vs ES `_analyze`) | unit green; parity within tolerance |
| **1** | Thin encrypted-path prototype: `sync-worker` writes a **new parallel** encrypted index (prod index untouched); `search-service` gains a flag-gated encrypted query path (A & B) | indexes + queries an encrypted message end-to-end |
| **2** | Both harnesses; run A/B/C; written **report + recommendation** | quality gate passes; A-vs-B decided on latency |
| **3** | Finish chosen approach: cut the real index over, drop ES `custom_analyzer` + plaintext content, reindex/migration plan, update `docs/client-api.md` | full tests, coverage ≥80%, SAST clean |

Phase 1 is deliberately non-destructive (parallel index, flagged path) so we benchmark against real services with C as a live control and zero prod-search risk.

## 10. Out of scope
- `spotlight` (room-name typeahead) and `user-room` (access-control) index encryption.
- Prefix / as-you-type matching on message search (D3).
- Live blind-key rotation machinery (D5 — seam only).
- Fuzzy/typo-tolerant and substring/infix matching (impossible/too costly over hashed tokens).

## 11. Prior art / alternatives considered

Industry approaches to "don't let the search backend expose readable message content," chosen by threat model. (Company specifics below are from general knowledge, not live-verified.)

| Pattern | Who (typical) | What | Rotation | Why not chosen here |
|---------|---------------|------|----------|---------------------|
| **At-rest + KMS envelope + RBAC** | Slack, Microsoft 365, standard Google Workspace | Index holds *readable* content; disk/snapshot encrypted via KMS-wrapped DEK; access is gated by strict RBAC + audit | Cheap — re-wrap the DEK, no re-encryption | Does **not** stop an authorized Kibana/cluster insider from reading content — the exact threat in D2. |
| **E2EE + client-side search** | WhatsApp, Signal, iMessage, Meta Messenger (E2EE) | Server has no searchable index; clients decrypt locally and search an **on-device** index | Per-conversation ratchet; no server index to rotate | Eliminates server-side search entirely — incompatible with our ES product requirement. |
| **Searchable / structured encryption (blind index, SSE)** | MongoDB Queryable Encryption; CipherStash; Acra; CipherSweet | Deterministic keyed hashing or formal SSE; server never sees plaintext | **No shortcut** — the hash *is* the stored artifact → rotation = reindex (versioned keys + background reindex + dual-version OR-query) | **This is our design (D1).** MongoDB QE is equality/range only, so it can't replace ES free-text search. |
| **Confidential computing / TEE** | MS SQL "Always Encrypted with secure enclaves"; bespoke on AWS Nitro / Azure confidential VMs | Decrypt only inside an attested enclave; full analyzer fidelity on plaintext *inside* the boundary | Arbitrary — plaintext is available in-enclave | ES is not enclave-native; attestation + perf overhead make this impractical to retrofit. |

**Two keys rotate differently (the core rotation insight):**
- **Content key** (readable blob) → envelope encryption (KEK wraps DEK). Rotate the KEK = re-wrap the DEK, **zero data re-encryption.** Already true via `pkg/atrest` + Vault.
- **Blind-index key** (search tokens) → **no industry shortcut exists.** Deterministic searchable encryption stores the hash itself, so a new key invalidates every stored hash. State of the art (CipherStash, Acra) = **versioned keys + reindex**, which is exactly D5's seam.

**Conclusion:** for self-hosted ES + threat model D2 ("insiders can't *read* content, keep server-side search"), blind indexing is the correct pragmatic choice and our rotation approach matches state-of-the-art. We would only pivot for a stricter threat model (→ E2EE + client-side search) or with investment in enclaves.

## 12. Key risks
- **CJK parity** — `cjk_bigram` divergence craters Chinese recall. Mitigated by a dedicated CJK query set + per-language gate + `_analyze` oracle.
- **search-service key surface (Approach A)** — A puts per-room `atrest` access into `search-service`. Acceptable per D6 latency priority; benchmark surfaces the tradeoff explicitly.
- **Reindex on cutover (Phase 3)** — historical plaintext docs must be reindexed into the blind index; needs a backfill plan (the existing `SYNC_MESSAGES_FROM` cutover mechanism is the model).
- **Two ciphertext copies (Approach A)** — message bodies encrypted in both Cassandra and ES. Same scheme, marginal blast-radius increase; noted for the security review.
