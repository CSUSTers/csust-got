package orm

import (
	"context"
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
	ctx := context.Background()
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
