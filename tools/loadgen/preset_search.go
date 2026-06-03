package main

import "time"

// SearchPreset is a fully-deterministic spec for the message-search workload.
// The query pool is multilingual (English, CJK, mixed) so the benchmark
// exercises the analyzer/blind-token path the encrypted arms depend on.
type SearchPreset struct {
	Name        string
	Rate        int           // target req/sec
	Duration    time.Duration // default run duration
	MaxInFlight int           // bounded in-flight requests
	Size        int           // page size requested per search
	Queries     []string      // multilingual query pool
}

// searchQueryPool is the shared multilingual query pool. It mixes single-token
// English, multi-token English phrases, and CJK terms/phrases so both the
// plaintext analyzer (arm C) and the blind tokenizer (arms A/B) are exercised.
var searchQueryPool = []string{
	"hello",
	"meeting",
	"project status",
	"please review",
	"deadline friday",
	"lunch plans",
	"release notes",
	"你好",
	"會議",
	"專案進度",
	"午餐",
	"截止日期",
	"請查看文件",
}

var searchPresets = map[string]SearchPreset{
	"search-small": {
		Name: "search-small", Rate: 20, Duration: 60 * time.Second,
		MaxInFlight: 50, Size: 20, Queries: searchQueryPool,
	},
	"search-medium": {
		Name: "search-medium", Rate: 100, Duration: 120 * time.Second,
		MaxInFlight: 200, Size: 20, Queries: searchQueryPool,
	},
	"search-large": {
		Name: "search-large", Rate: 500, Duration: 300 * time.Second,
		MaxInFlight: 500, Size: 20, Queries: searchQueryPool,
	},
}

// BuiltinSearchPreset looks up a search preset by name.
func BuiltinSearchPreset(name string) (SearchPreset, bool) {
	p, ok := searchPresets[name]
	return p, ok
}
