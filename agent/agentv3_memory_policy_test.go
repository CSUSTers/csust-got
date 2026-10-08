package agentv3

import (
	"strings"
	"testing"
	"time"

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

func TestAgentV3MemoryWriteDenial(t *testing.T) {
	chat := &tb.Chat{ID: -100, Type: tb.ChatSuperGroup}
	user := &tb.User{ID: 7}
	t.Run("non-admin is rejected under explicit_or_admin", func(t *testing.T) {
		scope := setupMemoryPolicyTest(t, "explicit_or_admin", 0, 20, false)
		denial, err := agentV3MemoryWriteDenial(t.Context(), scope, chat, user, "fact")
		require.NoError(t, err)
		require.Contains(t, denial, "只有管理员")
	})
	t.Run("admin passes under explicit_or_admin", func(t *testing.T) {
		scope := setupMemoryPolicyTest(t, "explicit_or_admin", 0, 20, true)
		denial, err := agentV3MemoryWriteDenial(t.Context(), scope, chat, user, "fact")
		require.NoError(t, err)
		require.Empty(t, denial)
	})
	t.Run("quota policy limits non-admin entries", func(t *testing.T) {
		scope := setupMemoryPolicyTest(t, "explicit_quota", 0, 1, false)
		denial, err := agentV3MemoryWriteDenial(t.Context(), scope, chat, user, "first")
		require.NoError(t, err)
		require.Empty(t, denial)
		require.NoError(t, addAgentV3Memory(t.Context(), scope, user.ID, "first"))
		require.NoError(t, addAgentV3Memory(t.Context(), scope, 8, "someone else"))
		denial, err = agentV3MemoryWriteDenial(t.Context(), scope, chat, user, "second")
		require.NoError(t, err)
		require.Contains(t, denial, "配额已用完（1/1 条）")
		denial, err = agentV3MemoryWriteDenial(t.Context(), scope, chat, &tb.User{ID: 9}, "fresh user")
		require.NoError(t, err)
		require.Empty(t, denial)
	})
	t.Run("full snapshot rejects new writes", func(t *testing.T) {
		scope := setupMemoryPolicyTest(t, "explicit_or_admin", 10, 20, true)
		require.NoError(t, addAgentV3Memory(t.Context(), scope, user.ID, strings.Repeat("a", 30)))
		denial, err := agentV3MemoryWriteDenial(t.Context(), scope, chat, user, strings.Repeat("b", 20))
		require.NoError(t, err)
		require.Contains(t, denial, "群记忆已满")
		require.Contains(t, denial, "/memory forget")
		denial, err = agentV3MemoryWriteDenial(t.Context(), scope, chat, user, "ok")
		require.NoError(t, err)
		require.Empty(t, denial)
	})
}

func TestMaybeRememberExplicitInputRepliesInsteadOfSilentlyFailing(t *testing.T) {
	scope := setupMemoryPolicyTest(t, "explicit_or_admin", 0, 20, false)
	var replies []string
	oldReply := agentV3MemoryReply
	agentV3MemoryReply = func(_ *TurnContext, text string) { replies = append(replies, text) }
	t.Cleanup(func() { agentV3MemoryReply = oldReply })

	msg := sessionMessage(10, 7, 0, "记住：group fact")
	tc := &TurnContext{Message: msg, ChatID: -100, V3: &AgentV3TurnState{Scope: scope}}
	require.NoError(t, maybeRememberExplicitInput(t.Context(), tc, "记住：group fact"))
	require.Len(t, replies, 1)
	require.Contains(t, replies[0], "只有管理员")
	items, err := orm.AgentV3ListMemory(t.Context(), scope)
	require.NoError(t, err)
	require.Empty(t, items)

	agentV3IsChatAdmin = func(*tb.Chat, *tb.User) bool { return true }
	require.NoError(t, maybeRememberExplicitInput(t.Context(), tc, "记住：group fact"))
	require.Len(t, replies, 1)
	items, err = orm.AgentV3ListMemory(t.Context(), scope)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int64(7), items[0].CreatedBy)
}

func TestJoinAgentV3MemoryLinesNewest(t *testing.T) {
	lines := []string{"- one", "- two", "- three"}
	tests := []struct {
		name  string
		limit int
		want  string
	}{
		{name: "unlimited", limit: 0, want: "- one\n- two\n- three"},
		{name: "fits", limit: 100, want: "- one\n- two\n- three"},
		{name: "drops oldest", limit: 14, want: "[earlier memory omitted: 1 entries]\n- two\n- three"},
		{name: "keeps only newest", limit: 8, want: "[earlier memory omitted: 2 entries]\n- three"},
		{name: "newest alone too long", limit: 4, want: truncateAgentV3Text("- three", 4)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, joinAgentV3MemoryLinesNewest(lines, tt.limit))
		})
	}
	require.Empty(t, joinAgentV3MemoryLinesNewest(nil, 10))
}

func TestRebuildAgentV3MemorySnapshotKeepsNewestEntriesWithoutTTL(t *testing.T) {
	scope := setupMemoryPolicyTest(t, "explicit_or_admin", 5, 20, true)
	mr := setupReplySessionRedis(t)
	config.BotConfig.AgentV3 = &config.AgentV3Config{Memory: config.AgentV3MemoryConfig{Enable: true, SnapshotMaxTokens: 5}}
	for _, content := range []string{"OLDEST_ENTRY", "MIDDLE_ENTRY", "NEWEST_ENTRY"} {
		require.NoError(t, addAgentV3Memory(t.Context(), scope, 7, content))
		time.Sleep(2 * time.Millisecond)
	}
	snapshot, err := orm.AgentV3GetMemorySnapshot(t.Context(), scope)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Contains(t, snapshot.Content, "NEWEST_ENTRY")
	require.NotContains(t, snapshot.Content, "OLDEST_ENTRY")
	require.Contains(t, snapshot.Content, "[earlier memory omitted")
	for _, key := range mr.Keys() {
		if strings.Contains(key, ":memory:") {
			require.Equal(t, time.Duration(0), mr.TTL(key), key)
		}
	}
}

func TestSaveAgentV3TurnPairSkipsRawTurnsOnSessionHit(t *testing.T) {
	setupReplySessionRedis(t)
	config.BotConfig.AgentV3 = &config.AgentV3Config{ContextCache: config.AgentV3ContextCacheConfig{RawTurns: 12, SummaryTurns: 80, RedisTTL: "1h"}}
	scope := orm.AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	msg := sessionMessage(10, 7, 0, "question")

	loaded := &TurnContext{Message: msg, ChatID: -100, V3: &AgentV3TurnState{Scope: scope, Trace: NewAgentV3Trace("run_hit", -100, 10)}, Session: &agentV3SessionTurn{parent: &session.LoadedParent{}}}
	require.True(t, agentV3SessionLoadedTurn(loaded))
	require.NoError(t, saveAgentV3TurnPair(t.Context(), loaded, "question", "answer", 11))
	turns, err := orm.AgentV3LoadTurns(t.Context(), scope, 12)
	require.NoError(t, err)
	require.Empty(t, turns, "a session hit must not append raw turns")
	summary, _, err := orm.AgentV3GetSummary(t.Context(), scope)
	require.NoError(t, err)
	require.Empty(t, summary)

	fresh := &TurnContext{Message: msg, ChatID: -100, V3: &AgentV3TurnState{Scope: scope}, Session: &agentV3SessionTurn{}}
	require.False(t, agentV3SessionLoadedTurn(fresh))
	require.NoError(t, saveAgentV3TurnPair(t.Context(), fresh, "question", "answer", 11))
	turns, err = orm.AgentV3LoadTurns(t.Context(), scope, 12)
	require.NoError(t, err)
	require.Len(t, turns, 2)

	background := &TurnContext{Background: true, Session: &agentV3SessionTurn{parent: &session.LoadedParent{}}}
	require.False(t, agentV3SessionLoadedTurn(background))
}
