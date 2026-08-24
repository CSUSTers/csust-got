package orm

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"csust-got/config"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupAgentV3Redis(t *testing.T) {
	t.Helper()
	mr := miniredis.RunT(t)
	old := rc
	rc = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rc.Close()
		rc = old
	})
	if config.BotConfig == nil {
		config.BotConfig = config.NewBotConfig()
	}
	config.BotConfig.RedisConfig.KeyPrefix = "test:"
}

func TestAgentV3PrefixTurnsMemoryAndTrace(t *testing.T) {
	setupAgentV3Redis(t)
	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	ttl := time.Hour

	rec := AgentV3PrefixRecord{
		Agent:          "agent",
		Model:          "model",
		Version:        1,
		Hash:           "prefix-hash",
		ToolDefsHash:   "tool-hash",
		PromptCacheKey: "cache-key",
		UpdatedAt:      time.Now(),
	}
	require.NoError(t, AgentV3SetPrefix(ctx, scope, rec, "stable-prefix", ttl))

	got, err := AgentV3GetPrefixCurrent(ctx, scope, "agent", "model")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "prefix-hash", got.Hash)
	msgs, err := AgentV3GetPrefixMessages(ctx, scope, 1)
	require.NoError(t, err)
	assert.Equal(t, "stable-prefix", msgs)

	require.NoError(t, AgentV3AppendTurn(ctx, scope, AgentV3Turn{Role: "user", Content: "one"}, 2, ttl))
	require.NoError(t, AgentV3AppendTurn(ctx, scope, AgentV3Turn{Role: "assistant", Content: "two"}, 2, ttl))
	require.NoError(t, AgentV3AppendTurn(ctx, scope, AgentV3Turn{Role: "user", Content: "three"}, 2, ttl))
	turns, err := AgentV3LoadTurns(ctx, scope, 2)
	require.NoError(t, err)
	require.Len(t, turns, 2)
	assert.Equal(t, "two", turns[0].Content)
	assert.Equal(t, "three", turns[1].Content)
	recentTurns, err := AgentV3LoadRecentTurns(ctx, scope, 3)
	require.NoError(t, err)
	require.Len(t, recentTurns, 3)
	assert.Equal(t, "one", recentTurns[0].Content)

	require.NoError(t, AgentV3SetSummary(ctx, scope, AgentV3Summary{
		Version: 1,
		Hash:    "summary-hash",
		Content: "- user: one",
	}, ttl))
	summaryContent, summaryVersion, err := AgentV3GetSummary(ctx, scope)
	require.NoError(t, err)
	assert.Equal(t, "- user: one", summaryContent)
	assert.Equal(t, int64(1), summaryVersion)

	require.NoError(t, AgentV3AddMemory(ctx, scope, AgentV3MemoryItem{
		ID:      "mem1",
		Content: "group fact",
	}, ttl))
	items, err := AgentV3ListMemory(ctx, scope)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "group fact", items[0].Content)

	snapshot := AgentV3MemorySnapshot{Version: 1, Hash: "h", Content: "- group fact"}
	require.NoError(t, AgentV3SetMemorySnapshot(ctx, scope, snapshot, ttl))
	gotSnapshot, err := AgentV3GetMemorySnapshot(ctx, scope)
	require.NoError(t, err)
	require.NotNil(t, gotSnapshot)
	assert.Equal(t, int64(1), gotSnapshot.Version)

	exitCode := 0
	summary := AgentV3TraceSummary{
		RunID:              "run_1",
		PrefixVersion:      1,
		CachedTokens:       32,
		LastBashExitCode:   &exitCode,
		LastBashDurationMS: 12,
		Spans: []AgentV3TraceSpanSummary{
			{Name: "context_cache", DurationMS: 2, Attrs: map[string]any{"cache_hit": true}},
			{Name: "tool_call", DurationMS: 10, Attrs: map[string]any{"tool": "bash"}},
		},
	}
	require.NoError(t, AgentV3SaveTraceSummary(ctx, scope, summary, ttl))
	gotSummary, err := AgentV3GetTraceSummary(ctx, scope)
	require.NoError(t, err)
	require.NotNil(t, gotSummary)
	assert.Equal(t, "run_1", gotSummary.RunID)
	assert.Equal(t, 32, gotSummary.CachedTokens)
	require.Len(t, gotSummary.Spans, 2)
	assert.Equal(t, "context_cache", gotSummary.Spans[0].Name)
	assert.Equal(t, true, gotSummary.Spans[0].Attrs["cache_hit"])
	assert.Equal(t, "bash", gotSummary.Spans[1].Attrs["tool"])
}

func TestAgentV3AppendTurnPairConcurrentKeepsPairsAdjacent(t *testing.T) {
	setupAgentV3Redis(t)
	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	const pairs = 48
	ttl := time.Hour

	start := make(chan struct{})
	errs := make(chan error, pairs)
	var wg sync.WaitGroup
	for id := range pairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- AgentV3AppendTurnPair(ctx, scope,
				AgentV3Turn{Role: "user", Content: fmt.Sprintf("user-%d", id), MessageID: id},
				AgentV3Turn{Role: "assistant", Content: fmt.Sprintf("assistant-%d", id), MessageID: id},
				pairs*2, ttl)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	for _, load := range []struct {
		name string
		fn   func() ([]AgentV3Turn, error)
	}{
		{"hot", func() ([]AgentV3Turn, error) { return AgentV3LoadTurns(ctx, scope, pairs*2) }},
		{"long", func() ([]AgentV3Turn, error) { return AgentV3LoadRecentTurns(ctx, scope, pairs*2) }},
	} {
		t.Run(load.name, func(t *testing.T) {
			turns, err := load.fn()
			require.NoError(t, err)
			require.Len(t, turns, pairs*2)

			seen := make(map[int]struct{}, pairs)
			for i := 0; i < len(turns); i += 2 {
				user := turns[i]
				assistant := turns[i+1]
				require.Equal(t, "user", user.Role)
				require.Equal(t, "assistant", assistant.Role)
				require.Equal(t, user.MessageID, assistant.MessageID)
				require.Equal(t, fmt.Sprintf("user-%d", user.MessageID), user.Content)
				require.Equal(t, fmt.Sprintf("assistant-%d", assistant.MessageID), assistant.Content)
				_, duplicate := seen[user.MessageID]
				require.False(t, duplicate, "pair %d was duplicated", user.MessageID)
				seen[user.MessageID] = struct{}{}
			}
			require.Len(t, seen, pairs)
		})
	}

	for _, key := range []string{agentV3TurnsKey(scope), agentV3HotRawTurnsKey(scope)} {
		remaining, err := rc.TTL(ctx, key).Result()
		require.NoError(t, err)
		assert.Positive(t, remaining)
	}
}
