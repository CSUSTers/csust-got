//go:build !386 && !arm

package chatv2

import (
	"testing"

	"csust-got/config"
	"csust-got/orm"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestSaveAgentV3TurnPairPersistsImageRefsForNextTurn(t *testing.T) {
	oldConfig := config.BotConfig
	testConfig := config.NewBotConfig()
	miniRedis := miniredis.RunT(t)
	testConfig.RedisConfig.RedisAddr = miniRedis.Addr()
	testConfig.RedisConfig.KeyPrefix = "agent-v3-image-refs:"
	testConfig.AgentV3.ContextCache = config.AgentV3ContextCacheConfig{RawTurns: 12, SummaryTurns: 12, RedisTTL: "1h"}
	config.BotConfig = testConfig
	orm.InitRedis()
	t.Cleanup(func() {
		config.BotConfig = oldConfig
		if oldConfig != nil && oldConfig.RedisConfig != nil {
			orm.InitRedis()
		}
	})

	tc := &TurnContext{
		Message: &tb.Message{ID: 42},
		V3: &AgentV3TurnState{
			Scope: orm.AgentV3Scope{Bot: "bot", Platform: agentV3Platform, ChatID: -100},
			ImageRefs: []orm.AgentV3ImageRef{
				{MessageID: 42, FileID: "telegram-file-id"},
			},
		},
	}
	require.NoError(t, saveAgentV3TurnPair(t.Context(), tc, "inspect this", "done", 99))

	turns, err := orm.AgentV3LoadTurns(t.Context(), tc.V3.Scope, 12)
	require.NoError(t, err)
	require.Len(t, turns, 2)
	assert.Equal(t, tc.V3.ImageRefs, turns[0].ImageRefs)
	nextTurnMessages := agentV3TurnsToMessages(turns)
	require.Len(t, nextTurnMessages, 2)
	assert.Contains(t, nextTurnMessages[0].Content, "file_id: telegram-file-id")
}
