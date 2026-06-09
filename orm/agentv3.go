package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type AgentV3Scope struct {
	Bot      string
	Platform string
	ChatID   int64
}

type AgentV3PrefixRecord struct {
	Agent                 string    `json:"agent"`
	Model                 string    `json:"model"`
	Version               int64     `json:"version"`
	Hash                  string    `json:"hash"`
	SoulHash              string    `json:"soul_hash"`
	MemorySnapshotHash    string    `json:"memory_snapshot_hash"`
	MemorySnapshotVersion int64     `json:"memory_snapshot_version"`
	ToolDefsHash          string    `json:"tool_defs_hash"`
	PromptCacheKey        string    `json:"prompt_cache_key"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type AgentV3Turn struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	MessageID int       `json:"message_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentV3MemoryItem struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	CreatedBy int64     `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentV3MemorySnapshot struct {
	Version   int64     `json:"version"`
	Hash      string    `json:"hash"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AgentV3Summary struct {
	Version   int64     `json:"version"`
	Hash      string    `json:"hash,omitempty"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type AgentV3TraceSummary struct {
	RunID                   string                    `json:"run_id"`
	ChatIDHash              string                    `json:"chat_id_hash"`
	MessageID               int                       `json:"message_id"`
	PrefixHash              string                    `json:"prefix_hash"`
	PrefixVersion           int64                     `json:"prefix_version"`
	PromptCacheKeyHash      string                    `json:"prompt_cache_key_hash"`
	PromptTokens            int                       `json:"prompt_tokens"`
	CachedTokens            int                       `json:"cached_tokens"`
	MemorySnapshotVersion   int64                     `json:"memory_snapshot_version"`
	SummaryVersion          int64                     `json:"summary_version"`
	RawTurnCount            int                       `json:"raw_turn_count"`
	ToolCallCount           int                       `json:"tool_call_count"`
	RuntimeNamespaceHash    string                    `json:"runtime_namespace_hash"`
	LastBashExitCode        *int                      `json:"last_bash_exit_code,omitempty"`
	LastBashDurationMS      int64                     `json:"last_bash_duration_ms,omitempty"`
	LastBashOutputTruncated bool                      `json:"last_bash_output_truncated,omitempty"`
	Error                   string                    `json:"error,omitempty"`
	StartedAt               time.Time                 `json:"started_at"`
	FinishedAt              time.Time                 `json:"finished_at"`
	Spans                   []AgentV3TraceSpanSummary `json:"spans,omitempty"`
}

type AgentV3TraceSpanSummary struct {
	Name       string         `json:"name"`
	DurationMS int64          `json:"duration_ms"`
	Error      string         `json:"error,omitempty"`
	Attrs      map[string]any `json:"attrs,omitempty"`
}

func AgentV3GetPrefixCurrent(ctx context.Context, scope AgentV3Scope, agent, model string) (*AgentV3PrefixRecord, error) {
	data, err := rc.Get(ctx, agentV3PrefixCurrentKey(scope, agent, model)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec AgentV3PrefixRecord
	if err := json.Unmarshal([]byte(data), &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func AgentV3SetPrefix(ctx context.Context, scope AgentV3Scope, rec AgentV3PrefixRecord, messages string, ttl time.Duration) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	pipe := rc.Pipeline()
	currentKey := agentV3PrefixCurrentKey(scope, rec.Agent, rec.Model)
	messagesKey := agentV3PrefixMessagesKey(scope, rec.Version)
	pipe.Set(ctx, currentKey, data, ttl)
	pipe.Set(ctx, messagesKey, messages, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func AgentV3GetPrefixMessages(ctx context.Context, scope AgentV3Scope, version int64) (string, error) {
	data, err := rc.Get(ctx, agentV3PrefixMessagesKey(scope, version)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return data, err
}

func AgentV3AppendTurn(ctx context.Context, scope AgentV3Scope, turn AgentV3Turn, maxTurns int, ttl time.Duration) error {
	if maxTurns <= 0 {
		maxTurns = 12
	}
	if turn.CreatedAt.IsZero() {
		turn.CreatedAt = time.Now()
	}
	data, err := json.Marshal(turn)
	if err != nil {
		return err
	}
	key := agentV3HotRawTurnsKey(scope)
	pipe := rc.Pipeline()
	pipe.RPush(ctx, agentV3TurnsKey(scope), data)
	pipe.Expire(ctx, agentV3TurnsKey(scope), ttl)
	pipe.RPush(ctx, key, data)
	pipe.LTrim(ctx, key, int64(-maxTurns), -1)
	pipe.Expire(ctx, key, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func AgentV3LoadTurns(ctx context.Context, scope AgentV3Scope, maxTurns int) ([]AgentV3Turn, error) {
	if maxTurns <= 0 {
		maxTurns = 12
	}
	items, err := rc.LRange(ctx, agentV3HotRawTurnsKey(scope), int64(-maxTurns), -1).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	turns := make([]AgentV3Turn, 0, len(items))
	for _, item := range items {
		var turn AgentV3Turn
		if err := json.Unmarshal([]byte(item), &turn); err != nil {
			continue
		}
		turns = append(turns, turn)
	}
	return turns, nil
}

func AgentV3LoadRecentTurns(ctx context.Context, scope AgentV3Scope, maxTurns int) ([]AgentV3Turn, error) {
	if maxTurns <= 0 {
		maxTurns = 80
	}
	items, err := rc.LRange(ctx, agentV3TurnsKey(scope), int64(-maxTurns), -1).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeAgentV3Turns(items), nil
}

func AgentV3GetSummary(ctx context.Context, scope AgentV3Scope) (string, int64, error) {
	data, err := rc.Get(ctx, agentV3SummaryCurrentKey(scope)).Result()
	if errors.Is(err, redis.Nil) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	var payload AgentV3Summary
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return "", 0, err
	}
	return payload.Content, payload.Version, nil
}

func AgentV3SetSummary(ctx context.Context, scope AgentV3Scope, summary AgentV3Summary, ttl time.Duration) error {
	if summary.UpdatedAt.IsZero() {
		summary.UpdatedAt = time.Now()
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	return rc.Set(ctx, agentV3SummaryCurrentKey(scope), data, ttl).Err()
}

func AgentV3AddMemory(ctx context.Context, scope AgentV3Scope, item AgentV3MemoryItem, ttl time.Duration) error {
	if item.ID == "" {
		item.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	pipe := rc.Pipeline()
	pipe.Set(ctx, agentV3MemoryItemKey(scope, item.ID), data, ttl)
	pipe.SAdd(ctx, agentV3MemoryActiveKey(scope), item.ID)
	pipe.Expire(ctx, agentV3MemoryActiveKey(scope), ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func AgentV3ListMemory(ctx context.Context, scope AgentV3Scope) ([]AgentV3MemoryItem, error) {
	ids, err := rc.SMembers(ctx, agentV3MemoryActiveKey(scope)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]AgentV3MemoryItem, 0, len(ids))
	for _, id := range ids {
		data, err := rc.Get(ctx, agentV3MemoryItemKey(scope, id)).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var item AgentV3MemoryItem
		if err := json.Unmarshal([]byte(data), &item); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func AgentV3ForgetMemory(ctx context.Context, scope AgentV3Scope, id string) error {
	pipe := rc.Pipeline()
	pipe.Del(ctx, agentV3MemoryItemKey(scope, id))
	pipe.SRem(ctx, agentV3MemoryActiveKey(scope), id)
	_, err := pipe.Exec(ctx)
	return err
}

func AgentV3SetMemorySnapshot(ctx context.Context, scope AgentV3Scope, snapshot AgentV3MemorySnapshot, ttl time.Duration) error {
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = time.Now()
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	pipe := rc.Pipeline()
	pipe.Set(ctx, agentV3MemorySnapshotCurrentKey(scope), data, ttl)
	pipe.Set(ctx, agentV3MemorySnapshotVersionKey(scope, snapshot.Version), data, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func AgentV3GetMemorySnapshot(ctx context.Context, scope AgentV3Scope) (*AgentV3MemorySnapshot, error) {
	data, err := rc.Get(ctx, agentV3MemorySnapshotCurrentKey(scope)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot AgentV3MemorySnapshot
	if err := json.Unmarshal([]byte(data), &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func AgentV3SaveTraceSummary(ctx context.Context, scope AgentV3Scope, summary AgentV3TraceSummary, ttl time.Duration) error {
	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	return rc.Set(ctx, agentV3TraceLastKey(scope), data, ttl).Err()
}

func AgentV3GetTraceSummary(ctx context.Context, scope AgentV3Scope) (*AgentV3TraceSummary, error) {
	data, err := rc.Get(ctx, agentV3TraceLastKey(scope)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var summary AgentV3TraceSummary
	if err := json.Unmarshal([]byte(data), &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

func agentV3BaseKey(scope AgentV3Scope) string {
	bot := scope.Bot
	if bot == "" {
		bot = "bot"
	}
	platform := scope.Platform
	if platform == "" {
		platform = "tg"
	}
	return wrapKey(fmt.Sprintf("agentv3:%s:%s:%d", bot, platform, scope.ChatID))
}

func decodeAgentV3Turns(items []string) []AgentV3Turn {
	turns := make([]AgentV3Turn, 0, len(items))
	for _, item := range items {
		var turn AgentV3Turn
		if err := json.Unmarshal([]byte(item), &turn); err != nil {
			continue
		}
		turns = append(turns, turn)
	}
	return turns
}

func agentV3PrefixCurrentKey(scope AgentV3Scope, agent, model string) string {
	return fmt.Sprintf("%s:prefix:current:%s:%s", agentV3BaseKey(scope), agent, model)
}

func agentV3PrefixMessagesKey(scope AgentV3Scope, version int64) string {
	return fmt.Sprintf("%s:prefix:%d:messages", agentV3BaseKey(scope), version)
}

func agentV3TurnsKey(scope AgentV3Scope) string {
	return agentV3BaseKey(scope) + ":turns"
}

func agentV3HotRawTurnsKey(scope AgentV3Scope) string {
	return agentV3BaseKey(scope) + ":hot:raw_turns"
}

func agentV3SummaryCurrentKey(scope AgentV3Scope) string {
	return agentV3BaseKey(scope) + ":summary:current"
}

func agentV3MemoryItemKey(scope AgentV3Scope, id string) string {
	return fmt.Sprintf("%s:memory:item:%s", agentV3BaseKey(scope), id)
}

func agentV3MemoryActiveKey(scope AgentV3Scope) string {
	return agentV3BaseKey(scope) + ":memory:active"
}

func agentV3MemorySnapshotCurrentKey(scope AgentV3Scope) string {
	return agentV3BaseKey(scope) + ":memory:snapshot:current"
}

func agentV3MemorySnapshotVersionKey(scope AgentV3Scope, version int64) string {
	return fmt.Sprintf("%s:memory:snapshot:%d", agentV3BaseKey(scope), version)
}

func agentV3TraceLastKey(scope AgentV3Scope) string {
	return agentV3BaseKey(scope) + ":trace:last"
}
