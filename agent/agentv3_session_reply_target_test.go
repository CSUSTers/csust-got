package agentv3

import (
	"testing"

	"csust-got/agent/session"
	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestAgentV3SessionBotReplyTargetDecision(t *testing.T) {
	bot := &tb.User{ID: 99, Username: "sessionbot", IsBot: true}
	chat := &tb.Chat{ID: -100}
	tests := []struct {
		name string
		tc   *TurnContext
		want int
	}{
		{"nil turn", nil, 0},
		{"no reply", &TurnContext{Message: &tb.Message{Chat: chat}, ChatID: -100, BotUser: bot}, 0},
		{"reply without sender", &TurnContext{Message: &tb.Message{Chat: chat, ReplyTo: &tb.Message{ID: 5}}, ChatID: -100, BotUser: bot}, 0},
		{"reply to another member", &TurnContext{Message: &tb.Message{Chat: chat, ReplyTo: &tb.Message{ID: 5, Sender: &tb.User{ID: 7}}}, ChatID: -100, BotUser: bot}, 0},
		{"reply to bot", &TurnContext{Message: &tb.Message{Chat: chat, ReplyTo: &tb.Message{ID: 5, Sender: bot}}, ChatID: -100, BotUser: bot}, 5},
		{"reply to bot with embedded chat", &TurnContext{Message: &tb.Message{Chat: chat, ReplyTo: &tb.Message{ID: 6, Chat: chat, Sender: &tb.User{ID: 99}}}, ChatID: -100, BotUser: bot}, 6},
		{"reply to bot in another chat", &TurnContext{Message: &tb.Message{Chat: chat, ReplyTo: &tb.Message{ID: 6, Chat: &tb.Chat{ID: -200}, Sender: bot}}, ChatID: -100, BotUser: bot}, 0},
		{"unknown bot identity", &TurnContext{Message: &tb.Message{Chat: chat, ReplyTo: &tb.Message{ID: 5, Sender: &tb.User{}}}, ChatID: -100, BotUser: &tb.User{}}, 0},
		{"missing bot user", &TurnContext{Message: &tb.Message{Chat: chat, ReplyTo: &tb.Message{ID: 5, Sender: bot}}, ChatID: -100}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := agentV3SessionBotReplyTarget(tt.tc)
			if tt.want == 0 {
				require.Nil(t, target)
				return
			}
			require.NotNil(t, target)
			require.Equal(t, tt.want, target.ID)
			require.EqualValues(t, -100, target.Chat.ID)
		})
	}
}

func TestAgentV3SessionBotReplyContinuesSessionRegardlessOfTrigger(t *testing.T) {
	f := newAgentSessionFixture(t)
	save := true
	cfg := &config.AgentConfig{Name: "bot-reply", ContextMode: "reply_chain", Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: false}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{
		{schema.AssistantMessage("ROOT_FINAL", nil)}, {schema.AssistantMessage("COMMAND_FINAL", nil)}, {schema.AssistantMessage("REGEX_FINAL", nil)},
		{schema.AssistantMessage("OTHER_FINAL", nil)}, {schema.AssistantMessage("MISSING_FINAL", nil)},
	}}
	f.compile(t, cfg, mdl)
	first := f.chat(t, cfg, sessionMessage(10, 7, 0, "ROOT_INPUT"), &config.AgentTrigger{Command: "ask"})
	root := f.node(t, first)
	noSave := false
	cfg.Session.SaveContext = &noSave
	require.False(t, cfg.Session.SaveEnabled())
	require.False(t, cfg.Session.LoadEnabled())

	command := sessionMessage(20, 7, 60, "COMMAND_INPUT")
	command.ReplyTo = &tb.Message{ID: first, Chat: command.Chat, Sender: f.bot.Me}
	second := f.chat(t, cfg, command, &config.AgentTrigger{Command: "ask"})
	secondNode := f.node(t, second)
	require.Equal(t, &root.Ref, secondNode.Parent, "a command replying to the bot continues that session and is saved")
	require.Contains(t, replySessionSchemaText(mdl.capturedInputs()[1]), "ROOT_INPUT")

	regex := sessionMessage(30, 8, 120, "REGEX_INPUT")
	regex.ReplyTo = &tb.Message{ID: second, Sender: &tb.User{ID: f.bot.Me.ID}}
	third := f.chat(t, cfg, regex, &config.AgentTrigger{Regex: "REGEX"})
	require.Equal(t, &secondNode.Ref, f.node(t, third).Parent, "a regex match replying to the bot continues the replied branch")
	require.Contains(t, replySessionSchemaText(mdl.capturedInputs()[2]), "COMMAND_INPUT")

	other := sessionMessage(40, 7, 180, "OTHER_INPUT")
	other.ReplyTo = sessionMessage(35, 8, 0, "MEMBER_TEXT")
	fourth := f.chat(t, cfg, other, &config.AgentTrigger{Command: "ask"})
	_, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: fourth})
	require.ErrorIs(t, err, session.ErrMiss, "replying to another member keeps the configured switches")
	require.NotContains(t, replySessionSchemaText(mdl.capturedInputs()[3]), "ROOT_INPUT")

	missing := sessionMessage(50, 7, 240, "MISSING_INPUT")
	missing.ReplyTo = &tb.Message{ID: 999999, Chat: missing.Chat, Sender: f.bot.Me}
	fifth := f.chat(t, cfg, missing, &config.AgentTrigger{Command: "ask"})
	require.Nil(t, f.node(t, fifth).Parent, "an unknown bot message degrades to the legacy context and still saves a new root")
	require.NotContains(t, replySessionSchemaText(mdl.capturedInputs()[4]), "ROOT_INPUT")
}

func TestRestoreAgentV3ReplayedRichSkill(t *testing.T) {
	old := config.BotConfig
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}
	t.Cleanup(func() { config.BotConfig = old })
	richCall := schema.ToolCall{ID: "load", Function: schema.FunctionCall{Name: agentV3ToolLoadSkill, Arguments: `{"name":"rich-message"}`}}
	otherCall := schema.ToolCall{ID: "other", Function: schema.FunctionCall{Name: agentV3ToolLoadSkill, Arguments: `{"name":"searxng"}`}}
	rich := &config.AgentConfig{Name: "rich", Agent: &config.AgentOptions{Enable: true, Rich: true}}
	plain := &config.AgentConfig{Name: "plain", Agent: &config.AgentOptions{Enable: true}}
	tests := []struct {
		name   string
		cfg    *config.AgentConfig
		replay []*schema.Message
		want   bool
	}{
		{"nil config", nil, []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{richCall}}}, false},
		{"rich disabled", plain, []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{richCall}}}, false},
		{"no load_skill call", rich, []*schema.Message{schema.UserMessage("hi"), schema.AssistantMessage("answer", nil)}, false},
		{"other skill", rich, []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{otherCall}}}, false},
		{"user-authored lookalike", rich, []*schema.Message{{Role: schema.User, ToolCalls: []schema.ToolCall{richCall}}}, false},
		{"rich skill loaded earlier", rich, []*schema.Message{schema.UserMessage("hi"), {Role: schema.Assistant, ToolCalls: []schema.ToolCall{otherCall, richCall}}, schema.AssistantMessage("answer", nil)}, true},
		{"nil message tolerated", rich, []*schema.Message{nil, {Role: schema.Assistant, ToolCalls: []schema.ToolCall{richCall}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := &TurnContext{Config: tt.cfg, V3: &AgentV3TurnState{}}
			require.Equal(t, tt.want, restoreAgentV3ReplayedRichSkill(tc, tt.replay))
			require.Equal(t, tt.want, tc.hasLoadedSkill(agentV3RichMessageSkillName))
		})
	}
}

func TestSessionCaptureModelResponsesCountsCompletedCalls(t *testing.T) {
	var nilCapture *SessionCapture
	require.Zero(t, nilCapture.ModelResponses())
	capture := NewSessionCapture()
	require.Zero(t, capture.ModelResponses())
	capture.record(schema.UserMessage("guidance"), true)
	require.Zero(t, capture.ModelResponses(), "guidance is not a model response")
	capture.record(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("call", `{}`)}}, false)
	capture.record(schema.ToolMessage("result", "call", schema.WithToolName("lookup")), false)
	require.Equal(t, 1, capture.ModelResponses())
	capture.record(schema.AssistantMessage("final", nil), false)
	require.Equal(t, 2, capture.ModelResponses())
}
