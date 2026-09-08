package agentv3

import (
	"strings"
	"testing"
	"time"

	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func setupReplySessionTurnConfig() *config.AgentV3Config {
	return &config.AgentV3Config{
		Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http", Endpoint: "http://runtime.invalid"},
		Skills:  config.AgentV3SkillsConfig{Mode: "system_prompt"},
		ContextCache: config.AgentV3ContextCacheConfig{
			RawTurns:     12,
			MaxRawTokens: 6000,
		},
	}
}

func replySessionTurnContext(cfg *config.AgentConfig, message *tb.Message) *TurnContext {
	return &TurnContext{
		Config:  cfg,
		Message: message,
		ChatID:  -100,
		BotUser: &tb.User{ID: 99, Username: "bot"},
	}
}

func TestLoadAgentHistoryContextModeGate(t *testing.T) {
	setupReplySessionRedis(t)
	old := sessionMessage(100, 7, 0, "FALLBACK_ONLY")
	require.NoError(t, orm.SetMessage(old))
	require.NoError(t, orm.PushMessageToStream(old))
	current := sessionMessage(101, 7, 1, "current")

	replyHistory, err := loadAgentHistory(replySessionTurnContext(&config.AgentConfig{ContextMode: "reply_chain"}, current))
	require.NoError(t, err)
	assert.Empty(t, replyHistory.ContextMessages)
	assert.Empty(t, replyHistory.FullMessages)

	chatHistory, err := loadAgentHistory(replySessionTurnContext(&config.AgentConfig{ContextMode: "chat", MessageContext: 2}, current))
	require.NoError(t, err)
	require.NotEmpty(t, chatHistory.ContextMessages)
	assert.Contains(t, chatHistory.ContextMessages[0].Text, "FALLBACK_ONLY")
}

func TestReplySessionPrepareExcludesChatSources(t *testing.T) {
	setupReplySessionRedis(t)
	config.BotConfig.AgentV3 = setupReplySessionTurnConfig()
	cfg := &config.AgentConfig{Name: "session", ContextMode: "reply_chain", Model: &config.Model{Model: "fixture"}}
	current := sessionMessage(42, 7, 0, "CURRENT_SENTINEL")
	tc := replySessionTurnContext(cfg, current)
	scope := orm.AgentV3Scope{Bot: "bot", Platform: agentV3Platform, ChatID: -100}
	require.NoError(t, orm.AgentV3AppendTurn(t.Context(), scope, orm.AgentV3Turn{
		Role: string(schema.User), Content: "RAW_SENTINEL", CreatedAt: time.Now(),
	}, 12, time.Hour))
	require.NoError(t, orm.AgentV3SetSummary(t.Context(), scope, orm.AgentV3Summary{
		Version: 1, Content: "SUMMARY_SENTINEL",
	}, time.Hour))
	history := &RichHistory{ContextMessages: []*ContextMessage{{ID: 1, Text: "FALLBACK_SENTINEL"}}}

	got, err := prepareAgentV3Turn(t.Context(), &CompiledAgent{Name: "session", Config: cfg}, tc, history)
	require.NoError(t, err)
	text := replySessionSchemaText(got)
	assert.NotContains(t, text, "RAW_SENTINEL")
	assert.NotContains(t, text, "SUMMARY_SENTINEL")
	assert.NotContains(t, text, "FALLBACK_SENTINEL")
	assert.Equal(t, 1, strings.Count(text, "CURRENT_SENTINEL"))
	require.NotNil(t, tc.V3)
	assert.Zero(t, tc.V3.SummaryVersion)
	assert.Zero(t, tc.V3.RawTurnCount)
}

func TestReplySessionPrepareSkipsInvalidChatHistoryKeys(t *testing.T) {
	tests := []struct {
		name      string
		corrupt   func(*redis.Client, orm.AgentV3Scope) error
		chatError string
	}{
		{
			name: "raw turns",
			corrupt: func(client *redis.Client, scope orm.AgentV3Scope) error {
				keys, err := client.Keys(t.Context(), "*:hot:raw_turns").Result()
				if err != nil || len(keys) != 1 {
					return err
				}
				if err := client.Del(t.Context(), keys[0]).Err(); err != nil {
					return err
				}
				return client.Set(t.Context(), keys[0], "wrong type", time.Hour).Err()
			},
			chatError: "raw turns",
		},
		{
			name: "summary",
			corrupt: func(client *redis.Client, scope orm.AgentV3Scope) error {
				keys, err := client.Keys(t.Context(), "*:summary:current").Result()
				if err != nil || len(keys) != 1 {
					return err
				}
				if err := client.Del(t.Context(), keys[0]).Err(); err != nil {
					return err
				}
				return client.RPush(t.Context(), keys[0], "wrong type").Err()
			},
			chatError: "summary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			miniRedis := setupReplySessionRedis(t)
			config.BotConfig.AgentV3 = setupReplySessionTurnConfig()
			scope := orm.AgentV3Scope{Bot: "bot", Platform: agentV3Platform, ChatID: -100}
			require.NoError(t, orm.AgentV3AppendTurn(t.Context(), scope, orm.AgentV3Turn{Role: string(schema.User), Content: "old"}, 12, time.Hour))
			require.NoError(t, orm.AgentV3SetSummary(t.Context(), scope, orm.AgentV3Summary{Content: "old"}, time.Hour))
			client := redis.NewClient(&redis.Options{Addr: miniRedis.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			require.NoError(t, tt.corrupt(client, scope))

			replyCfg := &config.AgentConfig{Name: "session", ContextMode: "reply_chain", Model: &config.Model{Model: "fixture"}}
			_, err := prepareAgentV3Turn(t.Context(), &CompiledAgent{Name: "session", Config: replyCfg}, replySessionTurnContext(replyCfg, sessionMessage(42, 7, 0, "reply")), nil)
			require.NoError(t, err)

			chatCfg := &config.AgentConfig{Name: "chat", ContextMode: "chat", Model: &config.Model{Model: "fixture"}}
			_, err = prepareAgentV3Turn(t.Context(), &CompiledAgent{Name: "chat", Config: chatCfg}, replySessionTurnContext(chatCfg, sessionMessage(43, 7, 1, "chat")), &RichHistory{})
			require.ErrorContains(t, err, tt.chatError)
		})
	}
}

func TestReplySessionPrepareKeepsCurrentMemoryButAllowsForget(t *testing.T) {
	setupReplySessionRedis(t)
	config.BotConfig.AgentV3 = setupReplySessionTurnConfig()
	config.BotConfig.AgentV3.Memory.Enable = true
	cfg := &config.AgentConfig{Name: "session", ContextMode: "reply_chain", Model: &config.Model{Model: "fixture"}}
	scope := orm.AgentV3Scope{Bot: "bot", Platform: agentV3Platform, ChatID: -100}
	require.NoError(t, addAgentV3Memory(t.Context(), scope, 7, "MEMORY_SENTINEL"))

	current := sessionMessage(42, 7, 0, "CURRENT_SENTINEL")
	got, err := prepareAgentV3Turn(t.Context(), &CompiledAgent{Name: "session", Config: cfg}, replySessionTurnContext(cfg, current), nil)
	require.NoError(t, err)
	text := replySessionSchemaText(got)
	assert.Less(t, strings.Index(text, "MEMORY_SENTINEL"), strings.Index(text, "CURRENT_SENTINEL"))

	items, err := orm.AgentV3ListMemory(t.Context(), scope)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, orm.AgentV3ForgetMemory(t.Context(), scope, items[0].ID))
	require.NoError(t, rebuildAgentV3MemorySnapshot(t.Context(), scope, config.BotConfig.AgentV3.ContextCacheTTL()))
	got, err = prepareAgentV3Turn(t.Context(), &CompiledAgent{Name: "session", Config: cfg}, replySessionTurnContext(cfg, current), nil)
	require.NoError(t, err)
	assert.NotContains(t, replySessionSchemaText(got), "MEMORY_SENTINEL")
}

func TestReplySessionPrepareUsesRedisChainRolesAndIDs(t *testing.T) {
	setupReplySessionRedis(t)
	config.BotConfig.AgentV3 = setupReplySessionTurnConfig()
	cfg := &config.AgentConfig{Name: "session", ContextMode: "reply_chain", Model: &config.Model{Model: "fixture"}}
	user := sessionMessage(10, 7, 0, "USER_REQUEST")
	bot := sessionMessage(11, 99, 1, "BOT_RESPONSE")
	bot.Sender.IsBot = true
	bot.ReplyTo = &tb.Message{ID: user.ID, Chat: user.Chat}
	for _, message := range []*tb.Message{user, bot} {
		require.NoError(t, orm.SetMessage(message))
		require.NoError(t, orm.PushMessageToStream(message))
	}
	current := sessionMessage(12, 7, 2, "CURRENT_REQUEST")
	current.ReplyTo = &tb.Message{ID: bot.ID, Chat: bot.Chat}

	got, err := prepareAgentV3Turn(t.Context(), &CompiledAgent{Name: "session", Config: cfg}, replySessionTurnContext(cfg, current), nil)
	require.NoError(t, err)
	text := replySessionSchemaText(got)
	assert.Contains(t, text, "message_id=\"10\"")
	assert.Contains(t, text, "message_id=\"11\"")
	assert.Contains(t, text, "message_id=\"12\"")
	assert.Contains(t, text, "USER_REQUEST")
	assert.Contains(t, text, "BOT_RESPONSE")
	assert.Contains(t, text, "CURRENT_REQUEST")
	roles := make([]schema.RoleType, 0, len(got))
	for _, message := range got {
		roles = append(roles, message.Role)
	}
	assert.Contains(t, roles, schema.Assistant)
	assert.Equal(t, 1, strings.Count(text, "CURRENT_REQUEST"))
}
