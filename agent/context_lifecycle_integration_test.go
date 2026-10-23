package agentv3

import (
	"context"
	"testing"
	"text/template"

	"csust-got/config"
	"csust-got/orm"

	"github.com/stretchr/testify/require"
)

func TestGetContextUsesTrustedLifecycleBeforeSelection(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(map[bool]string{false: "process", true: "final"}[final], func(t *testing.T) {
			setupReplySessionRedis(t)
			d := newDeliveryTelegram(t)
			parent := sessionMessage(10, 7, 0, "retained ancestor")
			process, err := d.bot.Send(parent.Chat, "...")
			require.NoError(t, err)
			RecordProcessMessage(d.bot, process, parent)
			if final {
				process.Text = "trusted delivered answer"
				require.NoError(t, SaveResponse(process, parent))
			}
			stale := *process
			stale.Chat = nil
			stale.Text = "stale process body"
			stale.ReplyTo = parent
			current := sessionMessage(process.ID+1, 7, 1, "followup")
			current.ReplyTo = &stale
			// A late inbound copy must not replace the trusted lifecycle snapshot.
			cached := *process
			cached.Text = "stale process body"
			require.NoError(t, orm.PushMessageToStream(&cached))
			tc := &TurnContext{Bot: d.bot, Message: current, ChatID: current.Chat.ID}
			ctx := WithTurnContext(t.Context(), tc)
			for _, args := range []string{`{"limit":1}`, `{"limit":1,"limit_from":"latest"}`} {
				output, err := (&getContextTool{}).InvokableRun(ctx, args)
				require.NoError(t, err)
				require.NotContains(t, output, "stale process body")
				if final {
					require.Contains(t, output, "trusted delivered answer")
				} else {
					require.Contains(t, output, "retained ancestor")
				}
			}
		})
	}
}

func TestAgentV3FreshTemplateQuoteUsesTrustedLifecycle(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(map[bool]string{false: "process", true: "final"}[final], func(t *testing.T) {
			setupReplySessionRedis(t)
			d := newDeliveryTelegram(t)
			parent := sessionMessage(10, 7, 0, "retained ancestor")
			process, err := d.bot.Send(parent.Chat, "...")
			require.NoError(t, err)
			RecordProcessMessage(d.bot, process, parent)
			if final {
				process.Text = "trusted delivered answer"
				require.NoError(t, SaveResponse(process, parent))
			}
			stale := *process
			stale.Text = "stale process body"
			stale.ReplyTo = parent
			current := sessionMessage(process.ID+1, 7, 1, "followup")
			current.ReplyTo = &stale
			cfg := &config.AgentConfig{Model: &config.Model{}}
			tc := &TurnContext{Bot: d.bot, BotUser: d.bot.Me, Message: current, ChatID: current.Chat.ID, Config: cfg}
			cc := &CompiledAgent{PromptTemplate: template.Must(template.New("quote").Parse(`{{.ReplyToXml}}`))}
			message, err := buildAgentV3UserMessage(cc, tc, nil, nil)
			require.NoError(t, err)
			require.NotContains(t, message.Content, "stale process body")
			if final {
				require.Contains(t, message.Content, "trusted delivered answer")
			} else {
				require.Contains(t, message.Content, "retained ancestor")
			}
			require.Equal(t, "stale process body", current.ReplyTo.Text)
		})
	}
}

func TestProgressSendRegistersTrustedProcessIdentity(t *testing.T) {
	setupReplySessionRedis(t)
	setupDeliveryConfig(t)
	d := newDeliveryTelegram(t)
	parent := sessionMessage(10, 7, 0, "request")
	tc := &TurnContext{Bot: d.bot, Message: parent, Config: configWithProgressSummary(), ChatID: parent.Chat.ID}
	ctx := WithTurnContext(t.Context(), tc)
	require.Equal(t, "ok", updateProgressMessage(ctx, updateProgressArgs{}, "working", wholeTextTypePlain))
	require.NotNil(t, tc.progressMsg)
	_, excluded, err := resolveContextMessage(t.Context(), tc.ChatID, tc.progressMsg)
	require.NoError(t, err)
	require.True(t, excluded)
}

func TestGetContextHonorsInvocationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	current := sessionMessage(20, 7, 0, "current")
	ctx = WithTurnContext(ctx, &TurnContext{Message: current, ChatID: current.Chat.ID})
	for _, args := range []string{`{"limit":1}`, `{"limit":1,"limit_from":"latest"}`} {
		output, err := (&getContextTool{}).InvokableRun(ctx, args)
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, output)
	}
}
