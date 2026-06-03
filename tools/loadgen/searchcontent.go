package main

import (
	"math/rand"
	"strings"
)

// searchVocab is the realistic multilingual vocabulary the load-test seeder
// draws message bodies from. It deliberately embeds every term in
// searchQueryPool (English single tokens, English phrases, and CJK terms) so a
// seeded corpus is guaranteed to contain documents the search-sustained query
// pool can match — otherwise every benchmark search returns zero hits and arms
// A/B never exercise the result-decrypt path. The remaining entries are filler
// chat vocabulary that gives the analyzer/blind-tokenizer realistic token
// variety without inflating the byte budget.
var searchVocab = func() []string {
	// Every query-pool term first, so coverage is structural, not incidental.
	v := append([]string{}, searchQueryPool...)
	v = append(v,
		// English filler — common chat/work vocabulary.
		"team", "update", "ticket", "bug", "fix", "merge", "branch", "deploy",
		"staging", "production", "rollback", "incident", "oncall", "standup",
		"sprint", "backlog", "estimate", "demo", "design", "spec", "draft",
		"approved", "blocked", "pending", "shipped", "thanks", "great", "sounds",
		"good", "agenda", "notes", "summary", "action", "items", "follow", "up",
		"schedule", "calendar", "invite", "room", "channel", "thread", "reply",
		"document", "report", "metrics", "latency", "throughput", "dashboard",
		"alert", "error", "retry", "timeout", "queue", "consumer", "publish",
		"index", "search", "query", "result", "match", "score", "ranking",
		// CJK filler — keeps the multilingual analyzer/bigram path warm.
		"系統", "訊息", "使用者", "登入", "通知", "推送", "上線", "離線",
		"資料", "報告", "進度", "完成", "處理", "問題", "修復", "測試",
		"部署", "版本", "更新", "團隊", "專案", "文件", "查看", "請",
		"今天", "明天", "公園", "散步", "天氣",
	)
	return v
}()

// searchableContent builds a deterministic, ~size-byte message body from
// searchVocab using r. It never exceeds size bytes and never cuts a multibyte
// rune in half (so output is always valid UTF-8). It replaces the old
// random-alphanumeric filler for the seeded corpus so search benchmarks return
// real hits while keeping the per-message byte budget the load presets rely on.
func searchableContent(r *rand.Rand, size int) string {
	if size <= 0 {
		return ""
	}
	var b strings.Builder
	for {
		w := searchVocab[r.Intn(len(searchVocab))]
		sep := 0
		if b.Len() > 0 {
			sep = 1
		}
		if b.Len()+sep+len(w) > size {
			if b.Len() > 0 {
				break
			}
			// size is smaller than the first chosen word: rune-safe truncate.
			return truncateRunes(w, size)
		}
		if sep == 1 {
			b.WriteByte(' ')
		}
		b.WriteString(w)
	}
	return b.String()
}

// truncateRunes returns the longest prefix of s whose byte length is <= max,
// cutting only on rune boundaries so the result stays valid UTF-8.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	for i := range s { // range over string yields rune-start byte indices
		if i > max {
			return s[:lastRuneStart(s, max)]
		}
	}
	if len(s) <= max {
		return s
	}
	return s[:lastRuneStart(s, max)]
}

// lastRuneStart returns the largest rune-start byte index <= max in s.
func lastRuneStart(s string, max int) int {
	if max >= len(s) {
		return len(s)
	}
	i := max
	for i > 0 && !utf8RuneStart(s[i]) {
		i--
	}
	return i
}

// utf8RuneStart reports whether b is the first byte of a UTF-8 rune (i.e. not a
// continuation byte 0b10xxxxxx).
func utf8RuneStart(b byte) bool {
	return b&0xC0 != 0x80
}
