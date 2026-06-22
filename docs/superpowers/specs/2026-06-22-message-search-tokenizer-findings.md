# Message search tokenizer — investigation findings

**Date:** 2026-06-22
**Scope:** verify the reported bug in `search-service` message search before designing a fix.
**Reported symptom:** "user searches `TMTV39-956A-43` and `TMTV39-956A-44` / `TMTV39-956A-45` also show up (legacy implementation)."

This is a findings note, NOT a design. Share with the team to confirm the real-world scenario before we pick a direction.

## What I tested

- Spun up `elasticsearch:8.17.0` locally.
- Recreated the production index template from `search-sync-worker/messages.go:174` exactly — `underscore_preserving` tokenizer + `underscore_subword` (word_delimiter_graph, `preserve_original: true`) + `cjk_bigram` + `lowercase`, `html_strip` char filter.
- Indexed test docs (full IDs + free-text variants).
- Ran the exact query from `search-service/query_messages.go:38-49`: `multi_match` / `type: bool_prefix` / `operator: AND` / `fields: ["content"]`.
- Also ran the legacy `type: phrase_prefix` for comparison.

## What I found

### 1. The full-ID search does NOT leak

| Query | Current `bool_prefix` AND | Legacy `phrase_prefix` |
|---|---|---|
| `TMTV39-956A-43` (full) | only doc 43 | only doc 43 |
| `TMTV39-956A-100` (full) | only doc 100 | only doc 100 |

Neither code path reproduces the reported symptom when the full ID is sent.

### 2. The leak DOES appear when a partial prefix is sent

| Query | `bool_prefix` AND | `phrase_prefix` |
|---|---|---|
| `TMTV39-956A-4` | 43, 44, 45 | 43, 44, 45 |
| `TMTV39-956A` | 43, 44, 45, 100 | 43, 44, 45, 100 |

Both legacy and current behave the same here. This is expected prefix-search behavior — `…-4` IS a prefix of `…-43/44/45`.

### 3. Likely real-world cause

The chat frontend almost certainly fires a search request on each keystroke. The user typed `…-4`, saw 43/44/45, and reported the leak — without realizing the request fired before they hit `3`. Worth confirming by:

- Checking the frontend search input — is it debounced / does it wait for Enter?
- Asking the reporter for the exact string in the search box at the moment 44/45 appeared.
- Looking at server logs for the actual `query` field received from the client.

### 4. Cross-tokenization bonus finding (separate issue)

The `word_delimiter_graph` filter with `preserve_original: true` makes hyphenated/underscored/dotted tokens **also** match space-separated variants:

| Indexed doc | `bool_prefix` query `aaa-bbb-cc` matches? |
|---|---|
| `aaa-bbb-cc` | yes |
| `aaa bbb cc` | **yes** (subword path matches `aaa`+`bbb`+prefix `cc`) |
| `aaa_bbb` (query) | matches `aaa_bbb`, `aaa+_bbb`, `aaa-bbb-cc` |

This is the "if input is `aaa-bbb-cc`, then both `aaa-bbb-cc` and `aaa bb cc` matched" behavior described in the original ticket — it's caused by the analyzer, not the choice of `bool_prefix` vs `phrase_prefix`. Both query types exhibit it.

This may or may not be desired behavior. Worth deciding deliberately rather than inheriting it.

## Reproducer (5 minutes, no codebase needed)

```bash
docker run -d --name es -p 9200:9200 \
  -e discovery.type=single-node -e xpack.security.enabled=false \
  -e ES_JAVA_OPTS='-Xms512m -Xmx512m' \
  docker.elastic.co/elasticsearch/elasticsearch:8.17.0

# wait for green
until curl -fsS localhost:9200/_cluster/health 2>/dev/null | grep -q '"status":"\(yellow\|green\)"'; do sleep 2; done

# create index with the production template
curl -s -X PUT localhost:9200/messages-test -H 'Content-Type: application/json' -d '{
  "settings": {"analysis": {
    "analyzer": {"custom_analyzer": {"type":"custom","tokenizer":"underscore_preserving","filter":["underscore_subword","cjk_bigram","lowercase"],"char_filter":["html_strip"]}},
    "tokenizer": {"underscore_preserving": {"type":"pattern","pattern":"[\\s,;!?()\\[\\]{}\"'\''<>]+"}},
    "filter": {"underscore_subword": {"type":"word_delimiter_graph","split_on_case_change":false,"split_on_numerics":false,"preserve_original":true}}
  }},
  "mappings": {"properties": {"content": {"type":"text","analyzer":"custom_analyzer"}}}
}'

curl -s -X POST localhost:9200/messages-test/_bulk -H 'Content-Type: application/json' --data-binary '
{"index":{"_id":"1"}}
{"content":"TMTV39-956A-43"}
{"index":{"_id":"2"}}
{"content":"TMTV39-956A-44"}
{"index":{"_id":"3"}}
{"content":"TMTV39-956A-45"}
'
sleep 1

# A) full ID — only doc 43 returned
curl -s localhost:9200/messages-test/_search -H 'Content-Type: application/json' -d '{
  "query":{"multi_match":{"query":"TMTV39-956A-43","type":"bool_prefix","operator":"AND","fields":["content"]}}}'

# B) partial — 43, 44, 45 all returned
curl -s localhost:9200/messages-test/_search -H 'Content-Type: application/json' -d '{
  "query":{"multi_match":{"query":"TMTV39-956A-4","type":"bool_prefix","operator":"AND","fields":["content"]}}}'
```

## Open questions before designing a fix

1. **Is the reporter's actual input the full string or a partial prefix?** If partial, behavior is "working as designed" for prefix search — the fix is on the frontend (debounce / require min length / require Enter).
2. **Is cross-tokenization (`aaa-bbb-cc` ↔ `aaa bbb cc`) desired or a bug?** Today both legacy and current exhibit it.
3. **Do we want mid-segment discovery?** e.g., should `956A` find `TMTV39-956A-43`? Today: yes (via subword filter). If we remove the subword filter to harden ID search, this stops working.

Once we have answers to 1–3, the design space is small (3 viable options) and we can pick one.
