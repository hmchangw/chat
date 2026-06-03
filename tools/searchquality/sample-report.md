# Encrypted Search Quality Report

Comparing the blind (encrypted) index against plaintext control index C.

- K (top-K cutoff): 10
- RBO persistence p: 0.90
- Gate: recall@10 >= 0.95 AND RBO >= 0.90 per language

## Per-language metrics

| Language | Queries | Recall@10 | Jaccard | RBO | Parity divergences | Gate |
|---|---|---|---|---|---|---|
| cjk | 4 | 1.000 | 0.000 | 0.000 | 5 | FAIL |
| english | 4 | 1.000 | 1.000 | 1.000 | 0 | PASS |
| html | 3 | 1.000 | 1.000 | 1.000 | 2 | PASS |
| mixed | 3 | 1.000 | 1.000 | 1.000 | 2 | PASS |

## Gate verdict: FAIL

## Parity divergences (msganalyzer.Analyze vs ES _analyze)

### cjk — 5 divergent doc(s)

- cjk-1: go=[今天 天天 天气 气很 很好 好我 我们 们去 去公 公园 园散 散步] es=[今天天气很好我们去公园散步]
- cjk-2: go=[公园 园里 里有 有很 很多 多人 人在 在散 散步 步和 和跑 跑步] es=[公园里有很多人在散步和跑步]
- cjk-3: go=[用户 name 登录 录系 系统 统后 后查 查看 看消 消息] es=[用户name登录系统后查看消息]

### html — 2 divergent doc(s)

- html-2: go=[world peace tagged content] es=[world & peace tagged content]
- html-4: go=[encrypted searchable message body] es=[encrypted & searchable message body]

### mixed — 2 divergent doc(s)

- mixed-1: go=[meeting 会议 at 3pm in room 会议 议室 a] es=[meeting 会议 at 3pm in room 会议室 a]
- mixed-3: go=[the 服务 务器 server restarted at midnight] es=[the 服务器 server restarted at midnight]

