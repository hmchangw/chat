package model_test

import (
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

func TestIsBotAccount(t *testing.T) {
	tests := []struct {
		name    string
		account string
		want    bool
	}{
		{name: "dot-bot suffix", account: "weather.bot", want: true},
		{name: "bare dot-bot", account: ".bot", want: true},
		{name: "p_ prefix webhook", account: "p_webhook", want: true},
		{name: "bare p_ prefix", account: "p_", want: true},
		{name: "plain human account", account: "alice", want: false},
		{name: "ends with bot but no dot", account: "robot", want: false},
		{name: "dot-bot not at end", account: "alice.bot.com", want: false},
		{name: "p underscore not at start", account: "alice_p_x", want: false},
		{name: "case sensitive prefix", account: "P_webhook", want: false},
		{name: "case sensitive suffix", account: "weather.BOT", want: false},
		{name: "empty", account: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := model.IsBotAccount(tt.account); got != tt.want {
				t.Errorf("IsBotAccount(%q) = %v, want %v", tt.account, got, tt.want)
			}
		})
	}
}
