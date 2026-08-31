package orm

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"csust-got/config"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupAgentV3Redis(t *testing.T) *miniredis.Miniredis {
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
	return mr
}

var agentV3UpdateSummaryUnderTest = AgentV3UpdateSummary

var errAgentV3StateConflictUnderTest = ErrAgentV3StateConflict

type agentV3MemorySnapshotBuilderUnderTest = AgentV3MemorySnapshotBuilder

var agentV3RebuildMemorySnapshotUnderTest = AgentV3RebuildMemorySnapshot

func buildAgentV3MemorySnapshotForTest(items []AgentV3MemoryItem, current *AgentV3MemorySnapshot) (*AgentV3MemorySnapshot, error) {
	contents := make([]string, 0, len(items))
	for _, item := range items {
		contents = append(contents, item.Content)
	}
	sort.Strings(contents)
	version := int64(1)
	if current != nil {
		version = current.Version + 1
	}
	return &AgentV3MemorySnapshot{
		Version: version,
		Content: strings.Join(contents, ","),
	}, nil
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

func TestAgentV3TurnImageRefsRoundTripAndLegacyJSON(t *testing.T) {
	setupAgentV3Redis(t)
	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	refs := []AgentV3ImageRef{{MessageID: 42, FileID: "telegram-file-id"}}

	require.NoError(t, AgentV3AppendTurn(ctx, scope, AgentV3Turn{
		Role:      "user",
		Content:   "inspect this image",
		MessageID: 42,
		ImageRefs: refs,
	}, 12, time.Hour))
	turns, err := AgentV3LoadTurns(ctx, scope, 12)
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, refs, turns[0].ImageRefs)

	var legacy AgentV3Turn
	require.NoError(t, json.Unmarshal([]byte(`{"role":"user","content":"legacy","message_id":7,"created_at":"2026-09-01T00:00:00Z"}`), &legacy))
	assert.Equal(t, "legacy", legacy.Content)
	assert.Empty(t, legacy.ImageRefs)
}

func TestAgentV3UpdateSummaryRetriesWholeReadComputeCAS(t *testing.T) {
	setupAgentV3Redis(t)
	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	ttl := time.Hour
	require.NoError(t, AgentV3SetSummary(ctx, scope, AgentV3Summary{
		Version: 1,
		Content: "v1",
	}, ttl))
	require.NoError(t, AgentV3AppendTurn(ctx, scope, AgentV3Turn{Role: "user", Content: "first"}, 12, ttl))

	firstBuilderEntered := make(chan int64, 1)
	releaseFirstBuilder := make(chan struct{})
	errCh := make(chan error, 1)
	calls := 0
	go func() {
		errCh <- agentV3UpdateSummaryUnderTest(ctx, scope, 80, ttl, func(turns []AgentV3Turn, current *AgentV3Summary) (*AgentV3Summary, error) {
			calls++
			require.NotNil(t, current)
			if calls == 1 {
				firstBuilderEntered <- current.Version
				<-releaseFirstBuilder
			}
			return &AgentV3Summary{
				Version: current.Version + 1,
				Content: fmt.Sprintf("summary-from-v%d-with-%d-turns", current.Version, len(turns)),
			}, nil
		})
	}()

	require.Equal(t, int64(1), <-firstBuilderEntered)
	concurrentUpdate := make(chan error, 1)
	go func() {
		if err := AgentV3SetSummary(ctx, scope, AgentV3Summary{Version: 2, Content: "v2"}, ttl); err != nil {
			concurrentUpdate <- err
			return
		}
		concurrentUpdate <- AgentV3AppendTurn(ctx, scope, AgentV3Turn{Role: "assistant", Content: "second"}, 12, ttl)
	}()
	require.NoError(t, <-concurrentUpdate)
	close(releaseFirstBuilder)
	require.NoError(t, <-errCh)

	assert.Equal(t, 2, calls)
	content, version, err := AgentV3GetSummary(ctx, scope)
	require.NoError(t, err)
	assert.Equal(t, int64(3), version)
	assert.Equal(t, "summary-from-v2-with-2-turns", content)
}

func TestAgentV3TraceLastNeverRegresses(t *testing.T) {
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

	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	ttl := time.Hour
	finished := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	newer := AgentV3TraceSummary{RunID: "run-b", FinishedAt: finished}
	olderTime := AgentV3TraceSummary{RunID: "run-z", FinishedAt: finished.Add(-time.Second)}
	olderTie := AgentV3TraceSummary{RunID: "run-a", FinishedAt: finished}

	require.NoError(t, AgentV3SaveTraceSummary(ctx, scope, newer, ttl))
	mr.FastForward(10 * time.Minute)
	before, err := rc.TTL(ctx, agentV3TraceLastKey(scope)).Result()
	require.NoError(t, err)

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, candidate := range []AgentV3TraceSummary{olderTime, olderTie} {
		go func() {
			<-start
			errs <- AgentV3SaveTraceSummary(ctx, scope, candidate, ttl)
		}()
	}
	close(start)
	for range 2 {
		require.NoError(t, <-errs)
	}
	after, err := rc.TTL(ctx, agentV3TraceLastKey(scope)).Result()
	require.NoError(t, err)
	assert.Equal(t, before, after)

	got, err := AgentV3GetTraceSummary(ctx, scope)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "run-b", got.RunID)

	require.NoError(t, AgentV3SaveTraceSummary(ctx, scope, AgentV3TraceSummary{RunID: "run-c", FinishedAt: finished}, ttl))
	got, err = AgentV3GetTraceSummary(ctx, scope)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "run-c", got.RunID)
	afterHigher, err := rc.TTL(ctx, agentV3TraceLastKey(scope)).Result()
	require.NoError(t, err)
	assert.Equal(t, ttl, afterHigher)
}

func TestAgentV3CASRetryExhaustionReturnsErrAgentV3StateConflict(t *testing.T) {
	setupAgentV3Redis(t)
	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	ttl := time.Hour
	require.NoError(t, AgentV3SetSummary(ctx, scope, AgentV3Summary{Version: 1, Content: "v1"}, ttl))

	attempts := 0
	err := agentV3UpdateSummaryUnderTest(ctx, scope, 80, ttl, func(_ []AgentV3Turn, current *AgentV3Summary) (*AgentV3Summary, error) {
		attempts++
		version := int64(1)
		if current != nil {
			version = current.Version + 1
		}
		require.NoError(t, AgentV3SetSummary(ctx, scope, AgentV3Summary{
			Version: version,
			Content: fmt.Sprintf("conflict-%d", attempts),
		}, ttl))
		return &AgentV3Summary{Version: version, Content: "stale"}, nil
	})

	assert.True(t, errors.Is(err, errAgentV3StateConflictUnderTest))
	assert.Equal(t, 5, attempts)
}

func TestAgentV3MemorySnapshotRetriesAfterConcurrentMutation(t *testing.T) {
	setupAgentV3Redis(t)
	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	ttl := time.Hour
	require.NoError(t, AgentV3AddMemory(ctx, scope, AgentV3MemoryItem{ID: "first", Content: "first"}, ttl))
	require.NoError(t, AgentV3SetMemorySnapshot(ctx, scope, AgentV3MemorySnapshot{Version: 1, Content: "first"}, ttl))

	firstBuilderEntered := make(chan struct{})
	releaseFirstBuilder := make(chan struct{})
	errCh := make(chan error, 1)
	calls := 0
	go func() {
		errCh <- agentV3RebuildMemorySnapshotUnderTest(ctx, scope, ttl, func(items []AgentV3MemoryItem, current *AgentV3MemorySnapshot) (*AgentV3MemorySnapshot, error) {
			calls++
			if calls == 1 {
				close(firstBuilderEntered)
				<-releaseFirstBuilder
			}
			return buildAgentV3MemorySnapshotForTest(items, current)
		})
	}()

	<-firstBuilderEntered
	require.NoError(t, AgentV3AddMemory(ctx, scope, AgentV3MemoryItem{ID: "second", Content: "second"}, ttl))
	close(releaseFirstBuilder)
	require.NoError(t, <-errCh)

	assert.Equal(t, 2, calls)
	snapshot, err := AgentV3GetMemorySnapshot(ctx, scope)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	assert.Greater(t, snapshot.Version, int64(1))
	assert.Equal(t, "first,second", snapshot.Content)
}

func TestAgentV3MemoryRebuildCleansStaleIDsAndAlignsTTLs(t *testing.T) {
	mr := setupAgentV3Redis(t)
	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	oldTTL := time.Hour
	ttl := 2 * time.Hour
	require.NoError(t, AgentV3AddMemory(ctx, scope, AgentV3MemoryItem{ID: "first", Content: "first"}, oldTTL))
	require.NoError(t, AgentV3AddMemory(ctx, scope, AgentV3MemoryItem{ID: "second", Content: "second"}, oldTTL))
	require.NoError(t, rc.SAdd(ctx, agentV3MemoryActiveKey(scope), "missing").Err())
	require.NoError(t, AgentV3SetMemorySnapshot(ctx, scope, AgentV3MemorySnapshot{Version: 7, Content: "old"}, oldTTL))

	require.NoError(t, agentV3RebuildMemorySnapshotUnderTest(ctx, scope, ttl, buildAgentV3MemorySnapshotForTest))

	stale, err := rc.SIsMember(ctx, agentV3MemoryActiveKey(scope), "missing").Result()
	require.NoError(t, err)
	assert.False(t, stale)

	snapshot, err := AgentV3GetMemorySnapshot(ctx, scope)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, int64(8), snapshot.Version)
	for _, key := range []string{
		agentV3MemoryItemKey(scope, "first"),
		agentV3MemoryItemKey(scope, "second"),
		agentV3MemoryActiveKey(scope),
		agentV3MemorySnapshotCurrentKey(scope),
		agentV3MemorySnapshotVersionKey(scope, snapshot.Version),
	} {
		assert.Equal(t, ttl, mr.TTL(key), key)
	}
}

func TestAgentV3MemoryRetryExhaustionReturnsConflict(t *testing.T) {
	setupAgentV3Redis(t)
	ctx := t.Context()
	scope := AgentV3Scope{Bot: "bot", Platform: "tg", ChatID: -100}
	ttl := time.Hour
	require.NoError(t, AgentV3AddMemory(ctx, scope, AgentV3MemoryItem{ID: "first", Content: "first"}, ttl))
	require.NoError(t, AgentV3SetMemorySnapshot(ctx, scope, AgentV3MemorySnapshot{Version: 1, Content: "baseline"}, ttl))

	attempts := 0
	err := agentV3RebuildMemorySnapshotUnderTest(ctx, scope, ttl, func(items []AgentV3MemoryItem, current *AgentV3MemorySnapshot) (*AgentV3MemorySnapshot, error) {
		attempts++
		if err := AgentV3AddMemory(ctx, scope, AgentV3MemoryItem{
			ID:      fmt.Sprintf("concurrent-%d", attempts),
			Content: "concurrent",
		}, ttl); err != nil {
			return nil, err
		}
		return buildAgentV3MemorySnapshotForTest(items, current)
	})

	assert.ErrorIs(t, err, errAgentV3StateConflictUnderTest)
	assert.Equal(t, 5, attempts)
	snapshot, getErr := AgentV3GetMemorySnapshot(ctx, scope)
	require.NoError(t, getErr)
	require.NotNil(t, snapshot)
	assert.Equal(t, int64(1), snapshot.Version)
	assert.Equal(t, "baseline", snapshot.Content)
}
