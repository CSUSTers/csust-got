package agentv3

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"csust-got/agent/session"
	"csust-got/config"
	"csust-got/orm"

	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func setupMemoryPolicyTest(t *testing.T, policy string, snapshotMaxTokens, maxPerUser int, admin bool) orm.AgentV3Scope {
	t.Helper()
	setupReplySessionRedis(t)
	config.BotConfig.AgentV3 = &config.AgentV3Config{Memory: config.AgentV3MemoryConfig{
		Enable: true, WritePolicy: policy, SnapshotMaxTokens: snapshotMaxTokens, MaxEntriesPerUser: maxPerUser,
	}}
	oldAdmin := agentV3IsChatAdmin
	agentV3IsChatAdmin = func(*tb.Chat, *tb.User) bool { return admin }
	t.Cleanup(func() { agentV3IsChatAdmin = oldAdmin })
	return orm.AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
}

func TestGroupMemoryPermissionAndCapacity(t *testing.T) {
	scope := setupMemoryPolicyTest(t, "explicit_or_admin", 10, 1, false)
	chat := &tb.Chat{ID: scope.ChatID, Type: tb.ChatSuperGroup}
	change := func(sender int64, action, id, text string) (string, error) {
		return manageGroupMemory(t.Context(), scope, chat, &tb.User{ID: sender}, action, "活动", "组会", "时间地点", id, text)
	}
	_, err := change(7, "add", "", "周五")
	require.Error(t, err)
	config.BotConfig.AgentV3.Memory.WritePolicy = "explicit_quota"
	first, err := change(7, "add", "", strings.Repeat("记", 20))
	require.NoError(t, err)
	_, err = change(7, "add", "", "第二条")
	require.Error(t, err)
	second, err := change(8, "add", "", strings.Repeat("忆", 15))
	require.NoError(t, err)
	_, err = change(7, "replace", second, "不是我的")
	require.Error(t, err)
	_, err = change(7, "forget", second, "")
	require.Error(t, err)
	_, err = change(7, "replace", first, strings.Repeat("新", 21))
	require.Error(t, err)
	retained, err := orm.ReadGroupMemory(t.Context(), scope, "活动", "组会", first)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("记", 20), retained.Content)
	id, err := change(7, "replace", first, strings.Repeat("新", 20))
	require.NoError(t, err)
	require.Equal(t, first, id)
	agentV3IsChatAdmin = func(*tb.Chat, *tb.User) bool { return true }
	_, err = change(7, "forget", second, "")
	require.NoError(t, err)
}

func TestGroupMemoryQuotaUnderConcurrentAdds(t *testing.T) {
	scope := setupMemoryPolicyTest(t, "explicit_quota", 0, 3, false)
	chat := &tb.Chat{ID: scope.ChatID, Type: tb.ChatSuperGroup}
	user := &tb.User{ID: 7}
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = manageGroupMemory(t.Context(), scope, chat, user, "add", "活动", "组会", "时间地点", "", fmt.Sprintf("fact %d", i))
		})
	}
	wg.Wait()
	var stored int
	for _, err := range errs {
		if err == nil {
			stored++
		}
	}
	catalog, err := orm.GroupMemoryCatalog(t.Context(), scope)
	require.NoError(t, err)
	require.Len(t, catalog, 1)
	require.Len(t, catalog[0].IDs, 3)
	require.Equal(t, 3, stored)
}

func TestSaveAgentV3TurnPairSkipsRawTurnsOnSessionHit(t *testing.T) {
	setupReplySessionRedis(t)
	config.BotConfig.AgentV3 = &config.AgentV3Config{ContextCache: config.AgentV3ContextCacheConfig{RawTurns: 12, SummaryTurns: 80, RedisTTL: "1h"}}
	scope := orm.AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	msg := sessionMessage(10, 7, 0, "question")

	loaded := &TurnContext{Message: msg, ChatID: -100, V3: &AgentV3TurnState{Scope: scope, Trace: NewAgentV3Trace("run_hit", -100, 10)}, Session: &agentV3SessionTurn{parent: &session.LoadedParent{}, committed: true}}
	require.True(t, agentV3SessionLoadedTurn(loaded))
	require.NoError(t, saveAgentV3TurnPair(t.Context(), loaded, "question", "answer", 11))
	turns, err := orm.AgentV3LoadTurns(t.Context(), scope, 12)
	require.NoError(t, err)
	require.Empty(t, turns, "a published session hit must not append raw turns")
	summary, _, err := orm.AgentV3GetSummary(t.Context(), scope)
	require.NoError(t, err)
	require.Empty(t, summary)

	uncommitted := &TurnContext{Message: msg, ChatID: -100, V3: &AgentV3TurnState{Scope: scope}, Session: &agentV3SessionTurn{parent: &session.LoadedParent{}}}
	require.False(t, agentV3SessionLoadedTurn(uncommitted), "a loaded parent whose commit failed is not a session hit")
	require.NoError(t, saveAgentV3TurnPair(t.Context(), uncommitted, "question", "answer", 11))
	turns, err = orm.AgentV3LoadTurns(t.Context(), scope, 12)
	require.NoError(t, err)
	require.Len(t, turns, 2, "the fallback context must keep a turn the DAG failed to publish")

	fresh := &TurnContext{Message: msg, ChatID: -100, V3: &AgentV3TurnState{Scope: scope}, Session: &agentV3SessionTurn{committed: true}}
	require.False(t, agentV3SessionLoadedTurn(fresh))
	require.NoError(t, saveAgentV3TurnPair(t.Context(), fresh, "question", "answer", 11))
	turns, err = orm.AgentV3LoadTurns(t.Context(), scope, 12)
	require.NoError(t, err)
	require.Len(t, turns, 4)

	background := &TurnContext{Background: true, Session: &agentV3SessionTurn{parent: &session.LoadedParent{}, committed: true}}
	require.False(t, agentV3SessionLoadedTurn(background))
}
