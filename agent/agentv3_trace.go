package agentv3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	agentV3TraceSaveTimeout      = 5 * time.Second
	agentV3TraceDefaultMaxSizeMB = 50
	agentV3TraceDefaultBackups   = 5
	agentV3TraceDefaultQueue     = 256
)

var (
	agentV3TraceSink               = make(chan struct{}, 1)
	agentV3TraceWriterRef          atomic.Pointer[agentV3TraceWriter]
	errAgentV3TracePayloadTooLarge = errors.New("agent v3 trace payload is too large")
	errAgentV3TraceQueueFull       = errors.New("agent v3 trace queue is full")
	errAgentV3TraceWriterClosed    = errors.New("agent v3 trace writer is closed")
)

func init() {
	agentV3TraceSink <- struct{}{}
}

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
	RedactContent           bool               `json:"-"`
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
		t.mu.Lock()
		if err != nil {
			span.Error = err.Error()
			if t.RedactContent {
				span.Error = "operation failed"
			}
			if t.Error == "" {
				t.Error = span.Error
			}
		}
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
		if t.RedactContent {
			t.Error = "operation failed"
		} else {
			t.Error = err.Error()
		}
	}
	t.mu.Unlock()
}

// SetContentRedacted controls whether trace content is replaced with safe summaries.
func (t *AgentV3Trace) SetContentRedacted(redacted bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.RedactContent = redacted
	t.mu.Unlock()
}

func agentV3TraceSaveContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), agentV3TraceSaveTimeout)
}

// Finish saves the agent-v3 trace summary and JSONL payload.
func (t *AgentV3Trace) Finish(ctx context.Context, scope orm.AgentV3Scope) {
	saveCtx, cancelSave := agentV3TraceSaveContext(ctx)
	defer cancelSave()
	t.finish(saveCtx, scope)
}

// FinishContext saves a trace without detaching from the caller's bounded context.
func (t *AgentV3Trace) FinishContext(ctx context.Context, scope orm.AgentV3Scope) {
	saveCtx, cancelSave := context.WithTimeout(ctx, agentV3TraceSaveTimeout)
	defer cancelSave()
	t.finish(saveCtx, scope)
}

func (t *AgentV3Trace) finish(saveCtx context.Context, scope orm.AgentV3Scope) {
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

	err := orm.AgentV3SaveTraceSummary(saveCtx, scope, summary, config.BotConfig.AgentV3.ContextCacheTTL())
	if err != nil {
		zap.L().Warn("agentv3: failed to save agent v3 trace summary",
			zap.String("run_id", t.RunID),
			zap.Error(err),
		)
	}
	if err := enqueueAgentV3TraceJSONL(saveCtx, config.BotConfig.AgentV3.Observability.JSONLPath, payload); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		zap.L().Warn("agentv3: failed to append agent v3 trace jsonl",
			zap.String("run_id", t.RunID),
			zap.String("path", config.BotConfig.AgentV3.Observability.JSONLPath),
			zap.Int64("dropped_total", agentV3TraceDropped()),
			zap.Error(err),
		)
	}
}

type agentV3TraceWriter struct {
	path        string
	out         *lumberjack.Logger
	queue       chan []byte
	done        chan struct{}
	mu          sync.RWMutex
	closed      bool
	dropped     atomic.Int64
	writeErrors atomic.Int64
}

func newAgentV3TraceWriter(path string, maxSizeMB, maxBackups, queueSize int) *agentV3TraceWriter {
	if maxSizeMB <= 0 {
		maxSizeMB = agentV3TraceDefaultMaxSizeMB
	}
	if maxBackups <= 0 {
		maxBackups = agentV3TraceDefaultBackups
	}
	if queueSize <= 0 {
		queueSize = agentV3TraceDefaultQueue
	}
	w := &agentV3TraceWriter{
		path:  path,
		out:   &lumberjack.Logger{Filename: path, MaxSize: maxSizeMB, MaxBackups: maxBackups, LocalTime: true},
		queue: make(chan []byte, queueSize),
		done:  make(chan struct{}),
	}
	go w.run()
	return w
}

func (w *agentV3TraceWriter) run() {
	defer close(w.done)
	for record := range w.queue {
		if err := w.write(record); err != nil {
			w.writeErrors.Add(1)
			zap.L().Warn("agentv3: trace writer failed to write record",
				zap.String("path", w.path),
				zap.Int64("write_errors", w.writeErrors.Load()),
				zap.Error(err),
			)
		}
	}
}

func (w *agentV3TraceWriter) write(record []byte) error {
	if err := tightenAgentV3TracePermissions(w.path); err != nil {
		return err
	}
	return writeAgentV3TraceRecord(w.out, record)
}

// Enqueue hands one JSON payload to the writer goroutine without blocking.
func (w *agentV3TraceWriter) Enqueue(payload []byte) error {
	record, err := agentV3TraceRecord(payload)
	if err != nil {
		return err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return errAgentV3TraceWriterClosed
	}
	select {
	case w.queue <- record:
		return nil
	default:
		w.dropped.Add(1)
		return errAgentV3TraceQueueFull
	}
}

// Dropped returns how many records were discarded because the queue was full.
func (w *agentV3TraceWriter) Dropped() int64 {
	return w.dropped.Load()
}

// Close stops accepting records, drains the queue and closes the file.
func (w *agentV3TraceWriter) Close(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return w.out.Close()
}

func initAgentV3TraceWriter() {
	closeAgentV3TraceWriter(context.Background())
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return
	}
	obs := config.BotConfig.AgentV3.Observability
	if !obs.Enable || obs.JSONLPath == "" {
		return
	}
	agentV3TraceWriterRef.Store(newAgentV3TraceWriter(obs.JSONLPath, obs.TraceMaxSizeMB, obs.TraceMaxBackups, obs.TraceQueue))
}

func closeAgentV3TraceWriter(ctx context.Context) {
	w := agentV3TraceWriterRef.Swap(nil)
	if w == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, agentV3TraceSaveTimeout)
	defer cancel()
	if err := w.Close(ctx); err != nil {
		zap.L().Warn("agentv3: trace writer did not flush cleanly",
			zap.String("path", w.path),
			zap.Int("pending", len(w.queue)),
			zap.Error(err),
		)
	}
	if dropped := w.Dropped(); dropped > 0 {
		zap.L().Warn("agentv3: trace records dropped because the queue was full",
			zap.String("path", w.path),
			zap.Int64("dropped_total", dropped),
		)
	}
}

func agentV3TraceDropped() int64 {
	if w := agentV3TraceWriterRef.Load(); w != nil {
		return w.Dropped()
	}
	return 0
}

func enqueueAgentV3TraceJSONL(ctx context.Context, path string, payload []byte) error {
	if path == "" || len(payload) == 0 {
		return nil
	}
	if w := agentV3TraceWriterRef.Load(); w != nil && w.path == path {
		return w.Enqueue(payload)
	}
	return appendAgentV3TraceJSONLContext(ctx, path, payload)
}

func tightenAgentV3TracePermissions(path string) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func agentV3TraceRecord(payload []byte) ([]byte, error) {
	recordSize, err := checkedAgentV3TraceRecordSize(len(payload))
	if err != nil {
		return nil, err
	}
	record := make([]byte, recordSize)
	copy(record, payload)
	record[len(payload)] = '\n'
	return record, nil
}

func appendAgentV3TraceJSONL(path string, payload []byte) error {
	return appendAgentV3TraceJSONLContext(context.Background(), path, payload)
}

func appendAgentV3TraceJSONLContext(ctx context.Context, path string, payload []byte) error {
	if path == "" || len(payload) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := agentV3TraceRecord(payload)
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-agentV3TraceSink:
	}
	defer func() { agentV3TraceSink <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tightenAgentV3TracePermissions(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writeErr := writeAgentV3TraceRecord(f, record)
	closeErr := f.Close()
	return errors.Join(writeErr, closeErr)
}

func checkedAgentV3TraceRecordSize(payloadSize int) (int, error) {
	if payloadSize > math.MaxInt-1 {
		return 0, errAgentV3TracePayloadTooLarge
	}
	return payloadSize + 1, nil
}

func writeAgentV3TraceRecord(w io.Writer, record []byte) error {
	n, err := w.Write(record)
	if err != nil {
		return err
	}
	if n != len(record) {
		return io.ErrShortWrite
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
