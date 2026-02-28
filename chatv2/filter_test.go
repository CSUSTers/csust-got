//go:build !386 && !arm

package chatv2

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
		filters []config.ChatFilterConfig
		want    bool
	}{
		{
			name: "nil message blocked",
			msg:  nil,
			filters: []config.ChatFilterConfig{
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
			filters: []config.ChatFilterConfig{
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
			filters: []config.ChatFilterConfig{
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
			filters: []config.ChatFilterConfig{
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
			filters: []config.ChatFilterConfig{
				{Type: "other_filter"},
			},
			want: true,
		},
		{
			name: "nil sender not in whitelist blocked without panic",
			msg: &tb.Message{
				Sender: nil,
				Chat:   &tb.Chat{ID: 888},
			},
			filters: []config.ChatFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100, 200}},
			},
			want: false,
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
			cfg := &config.ChatConfigSingle{
				Filters: config.ChatFilterSetting{
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
		filters []config.ChatFilterConfig
		wantLen int
	}{
		{
			name:    "empty config returns empty",
			filters: nil,
			wantLen: 0,
		},
		{
			name: "whitelist filter created",
			filters: []config.ChatFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100}},
			},
			wantLen: 1,
		},
		{
			name: "duplicate types deduplicated",
			filters: []config.ChatFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100}},
				{Type: "whitelist", Whitelist: []int64{200}},
			},
			wantLen: 1,
		},
		{
			name: "unknown type ignored",
			filters: []config.ChatFilterConfig{
				{Type: "unknown_filter"},
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ChatConfigSingle{
				Filters: config.ChatFilterSetting{
					Filters: tt.filters,
				},
			}
			result := buildFilters(cfg)
			assert.Len(t, result, tt.wantLen)
		})
	}
}

func TestProcessFilters(t *testing.T) {
	tests := []struct {
		name    string
		msg     *tb.Message
		filters []config.ChatFilterConfig
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
			filters: []config.ChatFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100}},
			},
			want: true,
		},
		{
			name: "non-whitelisted user blocked",
			msg:  &tb.Message{Sender: &tb.User{ID: 999}, Chat: &tb.Chat{ID: 1}},
			filters: []config.ChatFilterConfig{
				{Type: "whitelist", Whitelist: []int64{100}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ChatConfigSingle{
				Filters: config.ChatFilterSetting{
					Filters: tt.filters,
				},
			}
			ctx := &mockTbContext{msg: tt.msg}
			got := ProcessFilters(ctx, cfg)
			assert.Equal(t, tt.want, got)
		})
	}
}