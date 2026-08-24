//go:build !386 && !arm

package chatv2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const agentV3TraceSaveTimeout = 5 * time.Second

// AgentV3Trace records one agent-v3 run.
type AgentV3Trace struct {
	mu                      sync.Mutex
	RunID                   string             `json:"run_id"`
	ChatIDHash              string             `json:"chat_id_hash"`
	MessageID               int                `json:"message_id"`
	PrefixHash              string             `json:"prefix_hash"`
	PrefixVersion           int64              `json:"prefix_version"`
	PromptCacheKeyHash      string             `json:"prompt_cache_key_hash"`
	PromptTokens            int                `json:"prompt_tokens"`
	CachedTokens            int                `json:"cached_tokens"`
	MemorySnapshotVersion   int64              `json:"memory_snapshot_version"`
	SummaryVersion          int64              `json:"summary_version"`
	RawTurnCount            int                `json:"raw_turn_count"`
	ToolCallCount           int                `json:"tool_call_count"`
	RuntimeNamespaceHash    string             `json:"runtime_namespace_hash"`
	LastBashExitCode        *int               `json:"last_bash_exit_code,omitempty"`
	LastBashDurationMS      int64              `json:"last_bash_duration_ms,omitempty"`
	LastBashOutputTruncated bool               `json:"last_bash_output_truncated,omitempty"`
	Error                   string             `json:"error,omitempty"`
	StartedAt               time.Time          `json:"started_at"`
	FinishedAt              time.Time          `json:"finished_at"`
	Spans                   []AgentV3TraceSpan `json:"spans"`
}

// AgentV3TraceSpan records one timed agent-v3 operation.
type AgentV3TraceSpan struct {
	Name       string         `json:"name"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at"`
	DurationMS int64          `json:"duration_ms"`
	Error      string         `json:"error,omitempty"`
	Attrs      map[string]any `json:"attrs,omitempty"`
}

// NewAgentV3Trace creates an agent-v3 trace for one Telegram message.
func NewAgentV3Trace(runID string, chatID int64, messageID int) *AgentV3Trace {
	return &AgentV3Trace{
		RunID:      runID,
		ChatIDHash: hashString(fmtInt64(chatID)),
		MessageID:  messageID,
		StartedAt:  time.Now(),
		Spans:      []AgentV3TraceSpan{},
	}
}

// StartSpan starts a trace span and returns its finish function.
func (t *AgentV3Trace) StartSpan(name string, attrs map[string]any) func(error, map[string]any) {
	if t == nil {
		return func(error, map[string]any) {}
	}
	start := time.Now()
	return func(err error, endAttrs map[string]any) {
		finish := time.Now()
		merged := map[string]any{}
		for k, v := range attrs {
			merged[k] = v
		}
		for k, v := range endAttrs {
			merged[k] = v
		}
		span := AgentV3TraceSpan{
			Name:       name,
			StartedAt:  start,
			FinishedAt: finish,
			DurationMS: finish.Sub(start).Milliseconds(),
			Attrs:      merged,
		}
		if err != nil {
			span.Error = err.Error()
			t.SetError(err)
		}
		t.mu.Lock()
		t.Spans = append(t.Spans, span)
		t.mu.Unlock()
	}
}

// RecordUsage adds model token usage to the trace.
func (t *AgentV3Trace) RecordUsage(msg *schema.Message) {
	if t == nil || msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return
	}
	usage := msg.ResponseMeta.Usage
	t.mu.Lock()
	t.PromptTokens += usage.PromptTokens
	t.CachedTokens += usage.PromptTokenDetails.CachedTokens
	t.mu.Unlock()
}

// RecordToolCall increments the trace tool-call count.
func (t *AgentV3Trace) RecordToolCall() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.ToolCallCount++
	t.mu.Unlock()
}

// RecordBash records the latest bash execution summary.
func (t *AgentV3Trace) RecordBash(exitCode int, durationMS int64, truncated bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.LastBashExitCode = &exitCode
	t.LastBashDurationMS = durationMS
	t.LastBashOutputTruncated = truncated
	t.mu.Unlock()
}

// SetError stores the first trace error.
func (t *AgentV3Trace) SetError(err error) {
	if t == nil || err == nil {
		return
	}
	t.mu.Lock()
	if t.Error == "" {
		t.Error = err.Error()
	}
	t.mu.Unlock()
}

func agentV3TraceSaveContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), agentV3TraceSaveTimeout)
}

// Finish saves the agent-v3 trace summary and JSONL payload.
func (t *AgentV3Trace) Finish(ctx context.Context, scope orm.AgentV3Scope) {
	if t == nil || config.BotConfig == nil || config.BotConfig.AgentV3 == nil || !config.BotConfig.AgentV3.Observability.Enable {
		return
	}
	t.mu.Lock()
	t.FinishedAt = time.Now()
	summary := orm.AgentV3TraceSummary{
		RunID:                   t.RunID,
		ChatIDHash:              t.ChatIDHash,
		MessageID:               t.MessageID,
		PrefixHash:              t.PrefixHash,
		PrefixVersion:           t.PrefixVersion,
		PromptCacheKeyHash:      t.PromptCacheKeyHash,
		PromptTokens:            t.PromptTokens,
		CachedTokens:            t.CachedTokens,
		MemorySnapshotVersion:   t.MemorySnapshotVersion,
		SummaryVersion:          t.SummaryVersion,
		RawTurnCount:            t.RawTurnCount,
		ToolCallCount:           t.ToolCallCount,
		RuntimeNamespaceHash:    t.RuntimeNamespaceHash,
		LastBashExitCode:        t.LastBashExitCode,
		LastBashDurationMS:      t.LastBashDurationMS,
		LastBashOutputTruncated: t.LastBashOutputTruncated,
		Error:                   t.Error,
		StartedAt:               t.StartedAt,
		FinishedAt:              t.FinishedAt,
		Spans:                   compactAgentV3TraceSpans(t.Spans),
	}
	payload, _ := json.Marshal(t)
	t.mu.Unlock()

	saveCtx, cancelSave := agentV3TraceSaveContext(ctx)
	err := orm.AgentV3SaveTraceSummary(saveCtx, scope, summary, config.BotConfig.AgentV3.ContextCacheTTL())
	cancelSave()
	if err != nil {
		zap.L().Warn("chatv2: failed to save agent v3 trace summary",
			zap.String("run_id", t.RunID),
			zap.Error(err),
		)
	}
	if err := appendAgentV3TraceJSONL(config.BotConfig.AgentV3.Observability.JSONLPath, payload); err != nil {
		zap.L().Warn("chatv2: failed to append agent v3 trace jsonl",
			zap.String("run_id", t.RunID),
			zap.String("path", config.BotConfig.AgentV3.Observability.JSONLPath),
			zap.Error(err),
		)
	}
}

func appendAgentV3TraceJSONL(path string, payload []byte) error {
	if path == "" || len(payload) == 0 {
		return nil
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(payload); err != nil {
		return err
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		return err
	}
	return nil
}

func fmtInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func compactAgentV3TraceSpans(spans []AgentV3TraceSpan) []orm.AgentV3TraceSpanSummary {
	if len(spans) == 0 {
		return nil
	}
	out := make([]orm.AgentV3TraceSpanSummary, 0, len(spans))
	for _, span := range spans {
		out = append(out, orm.AgentV3TraceSpanSummary{
			Name:       span.Name,
			DurationMS: span.DurationMS,
			Error:      span.Error,
			Attrs:      compactAgentV3TraceAttrs(span.Attrs),
		})
	}
	return out
}

func compactAgentV3TraceAttrs(attrs map[string]any) map[string]any {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		switch x := v.(type) {
		case string:
			out[k] = truncateAgentV3Text(x, 240)
		case bool, int, int64, float64:
			out[k] = x
		default:
			out[k] = truncateAgentV3Text(fmtAny(x), 240)
		}
	}
	return out
}

func fmtAny(v any) string {
	data, err := json.Marshal(v)
	if err == nil {
		return string(data)
	}
	return fmt.Sprint(v)
}

func agentV3TracePreview(value string) (string, bool) {
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return "", false
	}
	cfg := config.BotConfig.AgentV3.Observability
	if cfg.CaptureContent != "preview" {
		return "", false
	}
	preview := truncateAgentV3Text(value, cfg.PreviewChars)
	if preview == "" {
		return "", false
	}
	return preview, true
}
