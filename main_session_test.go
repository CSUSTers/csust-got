package main

import (
	"csust-got/config"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	. "gopkg.in/telebot.v3"
)

var errFirstSessionRegex = errors.New("first regex selected")

func TestSessionTriggerNormalizedCopies(t *testing.T) {
	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(`
agents:
  - name: mixed-trigger
    session:
      save_context: false
      load_context: false
    trigger:
      - command: chat
        regex: ^hello$
        reply: true
`)))
	var agents config.AgentV3Configs
	require.NoError(t, v.UnmarshalKey("agents", &agents, viper.DecodeHook(config.DispatchFor())))
	require.Len(t, agents, 1)
	cfg := agents[0]
	require.Len(t, cfg.Trigger, 1)
	source := cfg.Trigger[0]
	original := *source
	tests := []struct {
		name string
		kind agentTriggerKind
		want config.AgentTrigger
	}{
		{name: "command", kind: agentTriggerCommand, want: config.AgentTrigger{Command: "chat"}},
		{name: "regex", kind: agentTriggerRegex, want: config.AgentTrigger{Regex: "^hello$"}},
		{name: "reply", kind: agentTriggerReply, want: config.AgentTrigger{Reply: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trigger := agentInvocationTrigger(source, tt.kind)
			require.Equal(t, tt.want, *trigger)
			require.NotSame(t, source, trigger)
			require.NotSame(t, trigger, agentInvocationTrigger(source, tt.kind))
			save, load := cfg.EffectiveSessionSettings(trigger)
			require.Equal(t, tt.want.Reply, save)
			require.Equal(t, tt.want.Reply, load)
			trigger.Command, trigger.Regex, trigger.Reply = "changed", "changed", false
			require.Equal(t, original, *source)
		})
	}
}

func TestCustomHandlerSessionTriggerPriorityAndCaption(t *testing.T) {
	originalConfig := config.BotConfig
	originalRegexHandlers := regexHandlers
	t.Cleanup(func() {
		config.BotConfig = originalConfig
		regexHandlers = originalRegexHandlers
	})
	config.BotConfig = config.NewBotConfig()
	config.BotConfig.AgentV3.Enable = true
	*config.BotConfig.Agents = config.AgentV3Configs{{
		Name:    "session-uncompiled-reply",
		Agent:   &config.AgentOptions{Enable: true},
		Trigger: []*config.AgentTrigger{{Command: "chat", Regex: "hello", Reply: true}},
	}}
	wantErr := errFirstSessionRegex
	regexHandlers = []struct {
		Regex *regexp.Regexp
		Func  func(Context) error
	}{
		{Regex: regexp.MustCompile("hello"), Func: func(Context) error { return wantErr }},
		{Regex: regexp.MustCompile(".*"), Func: func(Context) error { t.Fatal("later regex must not run"); return nil }},
	}
	tests := []struct {
		name    string
		text    string
		caption string
		want    error
	}{
		{name: "regex before reply", text: "hello", want: wantErr},
		{name: "caption regex before reply", caption: "hello", want: wantErr},
		{name: "text preferred over caption", text: "hello", caption: "other", want: wantErr},
		{name: "command with ReplyTo is not regex or reply", text: "/chat hello"},
		{name: "command with username and ReplyTo", text: "/chat@test_bot hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &mainTestContext{
				msg: &Message{
					Text: tt.text, Caption: tt.caption,
					Chat: &Chat{ID: 200}, Sender: &User{ID: 100},
					ReplyTo: &Message{Sender: &User{Username: "test_bot"}},
				},
				bot: &Bot{Me: &User{Username: "test_bot"}},
			}
			require.ErrorIs(t, customHandler(ctx), tt.want)
		})
	}
}
