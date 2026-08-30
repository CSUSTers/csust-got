package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	agentV3MaxStoredTurns = 1000
	agentV3CASMaxAttempts = 5
)

// ErrAgentV3StateConflict reports that bounded optimistic retries were exhausted.
var ErrAgentV3StateConflict = errors.New("agent v3 state changed during bounded retry")

// AgentV3Scope identifies one agent-v3 chat namespace.
type AgentV3Scope struct {
	Bot      string
	Platform string
	ChatID   int64
}

// AgentV3PrefixRecord stores current stable-prefix metadata.
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

// AgentV3Turn stores one user or assistant turn.
type AgentV3Turn struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	MessageID int       `json:"message_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AgentV3MemoryItem stores one chat-scoped memory item.
type AgentV3MemoryItem struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	CreatedBy int64     `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AgentV3MemorySnapshot stores rendered memory content for prompts.
type AgentV3MemorySnapshot struct {
	Version   int64     `json:"version"`
	Hash      string    `json:"hash"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentV3Summary stores the rolling agent-v3 conversation summary.
type AgentV3Summary struct {
	Version   int64     `json:"version"`
	Hash      string    `json:"hash,omitempty"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// AgentV3SummaryBuilder builds a replacement rolling summary from watched state.
type AgentV3SummaryBuilder func(turns []AgentV3Turn, current *AgentV3Summary) (*AgentV3Summary, error)

// AgentV3MemorySnapshotBuilder builds a replacement memory snapshot from watched state.
type AgentV3MemorySnapshotBuilder func(items []AgentV3MemoryItem, current *AgentV3MemorySnapshot) (*AgentV3MemorySnapshot, error)

// AgentV3TraceSummary stores compact trace metadata for the last run.
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

// AgentV3TraceSpanSummary stores compact trace span metadata.
type AgentV3TraceSpanSummary struct {
	Name       string         `json:"name"`
	DurationMS int64          `json:"duration_ms"`
	Error      string         `json:"error,omitempty"`
	Attrs      map[string]any `json:"attrs,omitempty"`
}

// AgentV3GetPrefixCurrent loads the current stable-prefix record.
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

// AgentV3SetPrefix stores stable-prefix metadata and messages.
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

// AgentV3GetPrefixMessages loads stable-prefix messages by version.
func AgentV3GetPrefixMessages(ctx context.Context, scope AgentV3Scope, version int64) (string, error) {
	data, err := rc.Get(ctx, agentV3PrefixMessagesKey(scope, version)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return data, err
}

// AgentV3AppendTurn appends a raw turn to agent-v3 history.
func AgentV3AppendTurn(ctx context.Context, scope AgentV3Scope, turn AgentV3Turn, maxTurns int, ttl time.Duration) error {
	if maxTurns <= 0 {
		maxTurns = 12
	}
	data, err := marshalAgentV3Turn(turn)
	if err != nil {
		return err
	}
	key := agentV3HotRawTurnsKey(scope)
	pipe := rc.Pipeline()
	pipe.RPush(ctx, agentV3TurnsKey(scope), data)
	pipe.LTrim(ctx, agentV3TurnsKey(scope), -agentV3MaxStoredTurns, -1)
	pipe.Expire(ctx, agentV3TurnsKey(scope), ttl)
	pipe.RPush(ctx, key, data)
	pipe.LTrim(ctx, key, int64(-maxTurns), -1)
	pipe.Expire(ctx, key, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

// AgentV3AppendTurnPair atomically appends a user and assistant turn to agent-v3 history.
func AgentV3AppendTurnPair(ctx context.Context, scope AgentV3Scope, userTurn, assistantTurn AgentV3Turn, maxTurns int, ttl time.Duration) error {
	if maxTurns <= 0 {
		maxTurns = 12
	}
	userData, err := marshalAgentV3Turn(userTurn)
	if err != nil {
		return err
	}
	assistantData, err := marshalAgentV3Turn(assistantTurn)
	if err != nil {
		return err
	}

	turnsKey := agentV3TurnsKey(scope)
	hotKey := agentV3HotRawTurnsKey(scope)
	pipe := rc.TxPipeline()
	pipe.RPush(ctx, turnsKey, userData, assistantData)
	pipe.LTrim(ctx, turnsKey, -agentV3MaxStoredTurns, -1)
	pipe.Expire(ctx, turnsKey, ttl)
	pipe.RPush(ctx, hotKey, userData, assistantData)
	pipe.LTrim(ctx, hotKey, int64(-maxTurns), -1)
	pipe.Expire(ctx, hotKey, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

// AgentV3LoadTurns loads recent raw turns for hot append.
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

// AgentV3LoadRecentTurns loads recent turns from the longer history list.
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

// AgentV3GetSummary loads the current conversation summary.
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

// AgentV3SetSummary stores the current conversation summary.
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

// AgentV3UpdateSummary atomically rebuilds the current conversation summary from recent turns.
func AgentV3UpdateSummary(ctx context.Context, scope AgentV3Scope, maxTurns int, ttl time.Duration, builder AgentV3SummaryBuilder) error {
	if builder == nil {
		return nil
	}
	if maxTurns <= 0 {
		maxTurns = 80
	}

	summaryKey := agentV3SummaryCurrentKey(scope)
	turnsKey := agentV3TurnsKey(scope)
	return agentV3WatchRetry(ctx, []string{summaryKey, turnsKey}, func(tx *redis.Tx) error {
		current, err := agentV3GetSummaryFromTx(ctx, tx, summaryKey)
		if err != nil {
			return err
		}
		turns, err := agentV3LoadRecentTurnsFromTx(ctx, tx, turnsKey, maxTurns)
		if err != nil {
			return err
		}
		next, err := builder(turns, current)
		if err != nil || next == nil {
			return err
		}
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, summaryKey, data, ttl)
			return nil
		})
		return err
	})
}

// AgentV3AddMemory stores one active memory item.
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

// AgentV3ListMemory lists active memory items.
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

// AgentV3ForgetMemory removes one active memory item.
func AgentV3ForgetMemory(ctx context.Context, scope AgentV3Scope, id string) error {
	pipe := rc.Pipeline()
	pipe.Del(ctx, agentV3MemoryItemKey(scope, id))
	pipe.SRem(ctx, agentV3MemoryActiveKey(scope), id)
	_, err := pipe.Exec(ctx)
	return err
}

// AgentV3SetMemorySnapshot stores rendered memory snapshot content.
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

// AgentV3RebuildMemorySnapshot atomically rebuilds a memory snapshot from active memory items.
func AgentV3RebuildMemorySnapshot(ctx context.Context, scope AgentV3Scope, ttl time.Duration, builder AgentV3MemorySnapshotBuilder) error {
	if builder == nil {
		return nil
	}

	activeKey := agentV3MemoryActiveKey(scope)
	currentKey := agentV3MemorySnapshotCurrentKey(scope)
	for range agentV3CASMaxAttempts {
		activeIDs, err := agentV3LoadMemoryActiveIDs(ctx, rc, activeKey)
		if err != nil {
			return err
		}
		watchKeys := make([]string, 0, len(activeIDs)+2)
		watchKeys = append(watchKeys, currentKey, activeKey)
		for _, id := range activeIDs {
			watchKeys = append(watchKeys, agentV3MemoryItemKey(scope, id))
		}

		err = rc.Watch(ctx, func(tx *redis.Tx) error {
			currentIDs, err := agentV3LoadMemoryActiveIDs(ctx, tx, activeKey)
			if err != nil {
				return err
			}
			if !agentV3MemoryActiveIDsEqual(activeIDs, currentIDs) {
				return redis.TxFailedErr
			}

			items := make([]AgentV3MemoryItem, 0, len(currentIDs))
			liveIDs := make([]string, 0, len(currentIDs))
			staleIDs := make([]string, 0)
			for _, id := range currentIDs {
				data, err := tx.Get(ctx, agentV3MemoryItemKey(scope, id)).Result()
				switch {
				case errors.Is(err, redis.Nil):
					staleIDs = append(staleIDs, id)
				case err != nil:
					return err
				default:
					var item AgentV3MemoryItem
					if err := json.Unmarshal([]byte(data), &item); err != nil {
						return err
					}
					items = append(items, item)
					liveIDs = append(liveIDs, id)
				}
			}

			current, err := agentV3GetMemorySnapshotFromTx(ctx, tx, currentKey)
			if err != nil {
				return err
			}
			next, err := builder(items, current)
			if err != nil || next == nil {
				return err
			}
			data, err := json.Marshal(next)
			if err != nil {
				return err
			}
			staleMembers := make([]interface{}, len(staleIDs))
			for i, id := range staleIDs {
				staleMembers[i] = id
			}

			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if len(staleIDs) > 0 {
					pipe.SRem(ctx, activeKey, staleMembers...)
				}
				for _, id := range liveIDs {
					pipe.Expire(ctx, agentV3MemoryItemKey(scope, id), ttl)
				}
				pipe.Expire(ctx, activeKey, ttl)
				pipe.Set(ctx, currentKey, data, ttl)
				pipe.Set(ctx, agentV3MemorySnapshotVersionKey(scope, next.Version), data, ttl)
				return nil
			})
			return err
		}, watchKeys...)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
	}
	return ErrAgentV3StateConflict
}

// AgentV3GetMemorySnapshot loads the current memory snapshot.
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

// AgentV3SaveTraceSummary stores the latest trace summary.
func AgentV3SaveTraceSummary(ctx context.Context, scope AgentV3Scope, summary AgentV3TraceSummary, ttl time.Duration) error {
	key := agentV3TraceLastKey(scope)
	return agentV3WatchRetry(ctx, []string{key}, func(tx *redis.Tx) error {
		currentData, err := tx.Get(ctx, key).Result()
		var current AgentV3TraceSummary
		switch {
		case errors.Is(err, redis.Nil):
		case err != nil:
			return err
		default:
			if err := json.Unmarshal([]byte(currentData), &current); err != nil {
				return err
			}
			if !agentV3TraceSummaryAfter(summary, current) {
				return nil
			}
		}

		data, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, data, ttl)
			return nil
		})
		return err
	})
}

// AgentV3GetTraceSummary loads the latest trace summary.
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

func agentV3WatchRetry(ctx context.Context, keys []string, callback func(*redis.Tx) error) error {
	for range agentV3CASMaxAttempts {
		err := rc.Watch(ctx, callback, keys...)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
	}
	return ErrAgentV3StateConflict
}

func agentV3GetSummaryFromTx(ctx context.Context, tx *redis.Tx, key string) (*AgentV3Summary, error) {
	data, err := tx.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var summary AgentV3Summary
	if err := json.Unmarshal([]byte(data), &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

func agentV3LoadMemoryActiveIDs(ctx context.Context, client redis.Cmdable, key string) ([]string, error) {
	ids, err := client.SMembers(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

func agentV3MemoryActiveIDsEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func agentV3GetMemorySnapshotFromTx(ctx context.Context, tx *redis.Tx, key string) (*AgentV3MemorySnapshot, error) {
	data, err := tx.Get(ctx, key).Result()
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

func agentV3LoadRecentTurnsFromTx(ctx context.Context, tx *redis.Tx, key string, maxTurns int) ([]AgentV3Turn, error) {
	items, err := tx.LRange(ctx, key, int64(-maxTurns), -1).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeAgentV3Turns(items), nil
}

func agentV3TraceSummaryAfter(candidate, current AgentV3TraceSummary) bool {
	return candidate.FinishedAt.After(current.FinishedAt) ||
		(candidate.FinishedAt.Equal(current.FinishedAt) && candidate.RunID > current.RunID)
}

func marshalAgentV3Turn(turn AgentV3Turn) ([]byte, error) {
	if turn.CreatedAt.IsZero() {
		turn.CreatedAt = time.Now()
	}
	return json.Marshal(turn)
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
