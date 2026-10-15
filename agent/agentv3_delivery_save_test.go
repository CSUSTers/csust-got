package agentv3

import (
	"context"
	"testing"

	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	openai "github.com/meguminnnnnnnnn/go-openai"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func newOfflineDeliveryBot(t *testing.T) *tb.Bot {
	t.Helper()
	bot, err := tb.NewBot(tb.Settings{Token: "delivery-save-token", Offline: true})
	require.NoError(t, err)
	return bot
}

func TestSaveAgentV3DeliveryOutlivesExpiredTurnContext(t *testing.T) {
	setupReplySessionRedis(t)
	config.BotConfig.AgentV3 = &config.AgentV3Config{ContextCache: config.AgentV3ContextCacheConfig{RawTurns: 12, SummaryTurns: 80, RedisTTL: "1h"}}
	scope := orm.AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	msg := sessionMessage(10, 7, 0, "question")
	tc := &TurnContext{Message: msg, ChatID: -100, V3: &AgentV3TurnState{Scope: scope}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	sent := &tb.Message{ID: 11, Chat: &tb.Chat{ID: -100}}

	saveAgentV3Delivery(ctx, newOfflineDeliveryBot(t).NewContext(tb.Update{Message: msg}), tc, nil, sent, "answer")

	turns, err := orm.AgentV3LoadTurns(t.Context(), scope, 12)
	require.NoError(t, err)
	require.Len(t, turns, 2, "post-delivery persistence must not inherit the expired turn deadline")
	require.Equal(t, "answer", turns[1].Content)
}

func TestSaveAgentV3DeliveryCachesEveryChunk(t *testing.T) {
	setupReplySessionRedis(t)
	user := sessionMessage(10, 7, 0, "question")
	chat := &tb.Chat{ID: -100}
	first := &tb.Message{ID: 11, Chat: chat, Text: "chunk one", ReplyTo: user}
	second := &tb.Message{ID: 12, Chat: chat, Text: "chunk two", ReplyTo: first}
	third := &tb.Message{ID: 13, Chat: chat, Text: "chunk three", ReplyTo: second}

	saveAgentV3Delivery(t.Context(), newOfflineDeliveryBot(t).NewContext(tb.Update{Message: user}), &TurnContext{ChatID: -100}, []*tb.Message{first, second, third}, third, "chunk one\nchunk two\nchunk three")

	for _, want := range []*tb.Message{first, second, third} {
		cached, err := orm.GetMessage(-100, want.ID)
		require.NoError(t, err)
		require.Equal(t, want.Text, cached.Text, "every chunk is cached with its own text")
		require.NotNil(t, cached.ReplyTo)
		require.Equal(t, want.ReplyTo.ID, cached.ReplyTo.ID, "the delivery reply chain is kept")
	}

	single := &tb.Message{ID: 14, Chat: chat, Text: "telegram text"}
	saveAgentV3Delivery(t.Context(), newOfflineDeliveryBot(t).NewContext(tb.Update{Message: user}), &TurnContext{ChatID: -100}, []*tb.Message{single}, single, "full response")
	cached, err := orm.GetMessage(-100, 14)
	require.NoError(t, err)
	require.Equal(t, "full response", cached.Text, "a single message keeps the full visible response")
}

func TestCommitAgentV3DeliveryNotesPartialDelivery(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "partial"}[partial], func(t *testing.T) {
			f := newAgentSessionFixture(t)
			cfg := &config.AgentConfig{Name: "partial", ContextMode: "reply_chain"}
			compiled := f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("FULL_ANSWER", nil)}}})
			tc := &TurnContext{Bot: f.bot, BotUser: f.bot.Me, Message: sessionMessage(10, 7, 0, "input"), ChatID: -100, Config: cfg}
			ctx := WithTurnContext(t.Context(), tc)
			setupAgentV3SessionTurn(tc)
			defer closeAgentV3SessionTurn(tc)
			messages, err := prepareAgentV3Turn(ctx, compiled, tc, nil)
			require.NoError(t, err)
			_, err = compiled.Agent.Generate(WithSessionCapture(ctx, tc.Session.capture), messages)
			require.NoError(t, err)

			delivery := telegramResponseResult{deliveredAll: []*tb.Message{{ID: 42}, {ID: 43}}}
			if partial {
				delivery.partial = &telegramPartialDelivery{sent: 2, total: 3, visible: "FULL"}
			}
			visible := commitAgentV3Delivery(tc, delivery, "FULL_ANSWER")

			delta := sessionRecordMessages(f.archive(t, f.node(t, 42)).Delta)
			last := delta[len(delta)-1]
			require.Equal(t, schema.Assistant, last.Role)
			if !partial {
				require.Equal(t, "FULL_ANSWER", visible)
				require.Equal(t, "FULL_ANSWER", last.Content, "a complete delivery archives no note")
				return
			}
			require.Equal(t, "FULL", visible, "the raw-turn fallback saves only the visible prefix")
			require.Equal(t, "FULL_ANSWER", delta[len(delta)-2].Content, "the sent answer is not rewritten")
			require.Equal(t, agentV3PartialDeliveryNote(delivery.partial), last.Content)
			require.Contains(t, last.Content, "仅前 2 段")
		})
	}
}

func TestAgentV3SessionLoadOnlyLaterContextLimitKeepsParent(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "loadonly", ContextMode: "reply_chain"}
	f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("ROOT_ANSWER", nil)}}})
	f.chat(t, cfg, sessionMessage(10, 7, 0, "root input"), nil)

	cfg.Session.LoadContext = true
	noSave := false
	cfg.Session.SaveContext = &noSave
	calls := 0
	overflow := &agentSessionModel{
		scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{sessionToolMessage("one")}}},
		before: func(context.Context, []*schema.Message) error {
			calls++
			if calls == 2 {
				return &openai.APIError{Code: "context_length_exceeded", HTTPStatusCode: 400, Message: "too long"}
			}
			return nil
		},
	}
	f.compile(t, cfg, overflow, lookupTool{})
	f.chat(t, cfg, sessionMessage(20, 7, 60, "overflow input"), &config.AgentTrigger{Command: "ask"})
	require.Equal(t, 2, calls, "the provider overflow happened after a tool round")
	require.Contains(t, replySessionSchemaText(overflow.capturedInputs()[0]), "ROOT_ANSWER")

	next := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("NEXT", nil)}}}
	f.compile(t, cfg, next)
	f.chat(t, cfg, sessionMessage(30, 7, 120, "next input"), &config.AgentTrigger{Command: "ask"})
	require.Contains(t, replySessionSchemaText(next.capturedInputs()[0]), "ROOT_ANSWER", "a load-only overflow must not reject the parent")
}
