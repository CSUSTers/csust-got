package agentv3

import (
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	tb "gopkg.in/telebot.v3"
)

// mockTbContext is a minimal tb.Context mock for filter tests.
type mockTbContext struct {
	tb.Context
	msg *tb.Message
}

func (m *mockTbContext) Message() *tb.Message { return m.msg }
func (m *mockTbContext) Chat() *tb.Chat {
	if m.msg != nil {
		return m.msg.Chat
	}
	return nil
}

func TestWhitelistFilter_Name(t *testing.T) {
	f := &whitelistFilter{}
	assert.Equal(t, "whitelist", f.Name())
}

func TestWhitelistFilter_Check(t *testing.T) {
	tests := []struct {
		name    string
		msg     *tb.Message
		filters []config.AgentFilterConfig
		want    bool
	}{
		{
			name: "nil message blocked",
			msg:  nil,
			filters: []config.AgentFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100}},
			},
			want: false,
		},
		{
			name: "user in whitelist allowed",
			msg: &tb.Message{
				Sender: &tb.User{ID: 100},
				Chat:   &tb.Chat{ID: 200},
			},
			filters: []config.AgentFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100}},
			},
			want: true,
		},
		{
			name: "chat ID in whitelist allowed",
			msg: &tb.Message{
				Sender: &tb.User{ID: 999},
				Chat:   &tb.Chat{ID: 200},
			},
			filters: []config.AgentFilterConfig{
				{Type: "whitelist", Whitelist: []int64{200}},
			},
			want: true,
		},
		{
			name: "user not in whitelist blocked",
			msg: &tb.Message{
				Sender: &tb.User{ID: 999},
				Chat:   &tb.Chat{ID: 888},
			},
			filters: []config.AgentFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100, 200}},
			},
			want: false,
		},
		{
			name: "no whitelist filter allows all",
			msg: &tb.Message{
				Sender: &tb.User{ID: 999},
				Chat:   &tb.Chat{ID: 888},
			},
			filters: []config.AgentFilterConfig{
				{Type: "other_filter"},
			},
			want: true,
		},
		{
			name: "empty filters allows all",
			msg: &tb.Message{
				Sender: &tb.User{ID: 999},
				Chat:   &tb.Chat{ID: 888},
			},
			filters: nil,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &whitelistFilter{}
			cfg := &config.AgentConfig{
				Filters: config.AgentFilterSettings{
					Filters: tt.filters,
				},
			}
			ctx := &mockTbContext{msg: tt.msg}
			got := f.Check(ctx, cfg)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildFilters(t *testing.T) {
	tests := []struct {
		name    string
		filters []config.AgentFilterConfig
		wantLen int
	}{
		{
			name:    "empty config returns empty",
			filters: nil,
			wantLen: 0,
		},
		{
			name: "whitelist filter created",
			filters: []config.AgentFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100}},
			},
			wantLen: 1,
		},
		{
			name: "duplicate types deduplicated",
			filters: []config.AgentFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100}},
				{Type: "whitelist", Whitelist: []int64{200}},
			},
			wantLen: 1,
		},
		{
			name: "unknown type ignored",
			filters: []config.AgentFilterConfig{
				{Type: "unknown_filter"},
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.AgentConfig{
				Filters: config.AgentFilterSettings{
					Filters: tt.filters,
				},
			}
			result := buildFilters(cfg)
			assert.Len(t, result, tt.wantLen)
		})
	}
}

func TestProcessFilters(t *testing.T) {
	originalConfig := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	t.Cleanup(func() { config.BotConfig = originalConfig })

	tests := []struct {
		name    string
		msg     *tb.Message
		filters []config.AgentFilterConfig
		want    bool
	}{
		{
			name:    "no filters allows all",
			msg:     &tb.Message{Sender: &tb.User{ID: 1}, Chat: &tb.Chat{ID: 1}},
			filters: nil,
			want:    true,
		},
		{
			name: "whitelisted user passes",
			msg:  &tb.Message{Sender: &tb.User{ID: 100}, Chat: &tb.Chat{ID: 1}},
			filters: []config.AgentFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100}},
			},
			want: true,
		},
		{
			name: "non-whitelisted user blocked",
			msg:  &tb.Message{Sender: &tb.User{ID: 999}, Chat: &tb.Chat{ID: 1}},
			filters: []config.AgentFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.AgentConfig{
				Filters: config.AgentFilterSettings{
					Filters: tt.filters,
				},
			}
			ctx := &mockTbContext{msg: tt.msg}
			got := ProcessFilters(ctx, cfg)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProcessFilters_EnabledGlobalWhitelistRejectsUnlistedGroup(t *testing.T) {
	originalConfig := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	t.Cleanup(func() { config.BotConfig = originalConfig })

	tests := []struct {
		name      string
		enabled   bool
		whitelist []int64
		msg       *tb.Message
		want      bool
	}{
		{
			name:    "disabled allows unlisted group",
			enabled: false,
			msg:     &tb.Message{Sender: &tb.User{ID: 100}, Chat: &tb.Chat{ID: 200}},
			want:    true,
		},
		{
			name:      "listed group allowed",
			enabled:   true,
			whitelist: []int64{200},
			msg:       &tb.Message{Sender: &tb.User{ID: 999}, Chat: &tb.Chat{ID: 200}},
			want:      true,
		},
		{
			name:      "listed sender cannot authorize unlisted group",
			enabled:   true,
			whitelist: []int64{100},
			msg:       &tb.Message{Sender: &tb.User{ID: 100}, Chat: &tb.Chat{ID: 200}},
			want:      false,
		},
		{
			name:      "unlisted group rejected",
			enabled:   true,
			whitelist: []int64{300},
			msg:       &tb.Message{Sender: &tb.User{ID: 999}, Chat: &tb.Chat{ID: 200}},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.BotConfig.WhiteListConfig.Enabled = tt.enabled
			config.BotConfig.WhiteListConfig.Chats = tt.whitelist

			got := ProcessFilters(&mockTbContext{msg: tt.msg}, &config.AgentConfig{})
			assert.Equalf(t, tt.want, got, "global whitelist enabled=%t chats=%v must authorize by chat ID", tt.enabled, tt.whitelist)
		})
	}
}
