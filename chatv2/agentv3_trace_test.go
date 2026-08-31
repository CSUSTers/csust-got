//go:build !386 && !arm

package chatv2

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"csust-got/config"
	"csust-got/orm"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type barrierTraceWriterState struct {
	mu      sync.Mutex
	data    []byte
	entered chan struct{}
	release <-chan struct{}
}

type barrierTraceWriter struct {
	state   *barrierTraceWriterState
	entered bool
}

func (w *barrierTraceWriter) Write(data []byte) (int, error) {
	w.state.mu.Lock()
	w.state.data = append(w.state.data, data...)
	w.state.mu.Unlock()
	if !w.entered {
		w.entered = true
		w.state.entered <- struct{}{}
		<-w.state.release
	}
	return len(data), nil
}

func (s *barrierTraceWriterState) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...)
}

func requireCompleteAgentV3TraceJSONL(t *testing.T, data []byte, wantCount int) {
	t.Helper()
	lines := splitNonEmptyJSONLLines(data)
	require.Len(t, lines, wantCount)
	seen := make(map[int]struct{}, wantCount)
	for _, line := range lines {
		var record struct {
			ID int `json:"id"`
		}
		require.NoError(t, json.Unmarshal(line, &record))
		_, duplicate := seen[record.ID]
		require.False(t, duplicate, "duplicate trace record id %d", record.ID)
		seen[record.ID] = struct{}{}
	}
	require.Len(t, seen, wantCount)
	for id := range wantCount {
		_, ok := seen[id]
		require.True(t, ok, "missing trace record id %d", id)
	}
}

func splitNonEmptyJSONLLines(data []byte) [][]byte {
	lines := make([][]byte, 0)
	start := 0
	for i, b := range data {
		if b != '\n' {
			continue
		}
		if i > start {
			lines = append(lines, data[start:i])
		}
		start = i + 1
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func TestAppendAgentV3TraceJSONLForcedPayloadNewlineInterleave(t *testing.T) {
	release := make(chan struct{})
	state := &barrierTraceWriterState{
		entered: make(chan struct{}, 2),
		release: release,
	}
	errs := make(chan error, 2)
	for id := range 2 {
		go func() {
			errs <- writeAgentV3TraceRecord(
				&barrierTraceWriter{state: state},
				append([]byte(`{"id":`+string(rune('0'+id))+`}`), '\n'),
			)
		}()
	}
	for range 2 {
		select {
		case <-state.entered:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for both payload writes")
		}
	}
	close(release)
	for range 2 {
		require.NoError(t, <-errs)
	}

	requireCompleteAgentV3TraceJSONL(t, state.bytes(), 2)
}

func TestAppendAgentV3TraceJSONLConcurrentRecords(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "traces", "agentv3.jsonl")
	errs := make(chan error, 128)
	var writers sync.WaitGroup
	for id := range 128 {
		writers.Go(func() {
			errs <- appendAgentV3TraceJSONL(tracePath, []byte(`{"id":`+strconv.Itoa(id)+`}`))
		})
	}
	writers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	data, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	requireCompleteAgentV3TraceJSONL(t, data, 128)
}

func TestAppendAgentV3TraceJSONLTightensPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix owner modes are not represented on Windows")
	}
	traceDir := filepath.Join(t.TempDir(), "traces")
	tracePath := filepath.Join(traceDir, "agentv3.jsonl")
	require.NoError(t, os.MkdirAll(traceDir, 0o755))
	require.NoError(t, os.WriteFile(tracePath, []byte("old\n"), 0o644))
	require.NoError(t, os.Chmod(traceDir, 0o755))
	require.NoError(t, os.Chmod(tracePath, 0o644))

	require.NoError(t, appendAgentV3TraceJSONL(tracePath, []byte(`{"id":1}`)))
	assert.Equal(t, fs.FileMode(0o700), mustAgentV3TraceMode(t, traceDir).Perm())
	assert.Equal(t, fs.FileMode(0o600), mustAgentV3TraceMode(t, tracePath).Perm())
}

func mustAgentV3TraceMode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Mode()
}

type tracePreviewTestTool struct {
	result string
}

func (tracePreviewTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{}, nil
}

func (t tracePreviewTestTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return t.result, nil
}

func TestAgentV3TracePersistsSensitiveToolPreviewPolicy(t *testing.T) {
	oldConfig := config.BotConfig
	testConfig := config.NewBotConfig()
	miniRedis := miniredis.RunT(t)
	testConfig.RedisConfig.RedisAddr = miniRedis.Addr()
	testConfig.RedisConfig.KeyPrefix = "agent-v3-trace-preview:"
	tracePath := filepath.Join(t.TempDir(), "agentv3.jsonl")
	testConfig.AgentV3 = &config.AgentV3Config{
		Observability: config.AgentV3ObservabilityConfig{
			Enable:         true,
			JSONLPath:      tracePath,
			CaptureContent: "preview",
			PreviewChars:   512,
		},
	}
	config.BotConfig = testConfig
	orm.InitRedis()
	t.Cleanup(func() {
		config.BotConfig = oldConfig
		if oldConfig != nil && oldConfig.RedisConfig != nil {
			orm.InitRedis()
		}
	})

	calls := []struct {
		name   string
		args   string
		result string
	}{
		{agentV3ToolLoadSkill, `{"name":"fixture-skill","token":"fixture-secret-token"}`, "fixture-skill-body fixture-secret-token"},
		{agentV3ToolSearXNGWebSearch, `{"query":"fixture-private-query","token":"fixture-secret-token"}`, "fixture-private-result https://search.fixture.invalid/private-result fixture-secret-token"},
		{agentV3ToolSearXNGSuggestions, `{"query":"fixture-private-query","token":"fixture-secret-token"}`, "fixture-private-query-suggestion fixture-secret-token"},
		{agentV3ToolSearXNGInstanceInfo, `{"include_engines":true,"token":"fixture-secret-token"}`, "fixture-instance-result https://search.fixture.invalid/instance fixture-secret-token"},
		{"fixture_generic_tool", `{"query":"fixture-generic-args"}`, "fixture-generic-result"},
	}
	trace := NewAgentV3Trace("trace-preview-test", -100, 42)
	agent := &CustomAgent{invokables: make(map[string]tool.InvokableTool, len(calls))}
	for _, call := range calls {
		agent.invokables[call.name] = tracePreviewTestTool{result: call.result}
	}
	scope := orm.AgentV3Scope{Bot: "test-bot", Platform: agentV3Platform, ChatID: -100}
	ctx := WithTurnContext(t.Context(), &TurnContext{V3: &AgentV3TurnState{Trace: trace}})

	for _, call := range calls {
		msg := agent.executeToolCall(ctx, schema.ToolCall{
			ID: "call-" + call.name,
			Function: schema.FunctionCall{
				Name:      call.name,
				Arguments: call.args,
			},
		})
		require.Equal(t, call.result, msg.Content)
	}
	trace.Finish(ctx, scope)

	data, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	var persisted AgentV3Trace
	require.NoError(t, json.Unmarshal(data, &persisted))
	assert.Equal(t, len(calls), persisted.ToolCallCount)

	for _, marker := range []string{
		"fixture-skill-body",
		"fixture-private-query",
		"fixture-private-result",
		"fixture-secret-token",
		"https://search.fixture.invalid/",
	} {
		assert.NotContains(t, string(data), marker)
	}

	for _, call := range calls[:4] {
		attrs := persistedAgentV3ToolSpanAttrs(t, persisted.Spans, call.name)
		assert.Equal(t, call.name, attrs["tool"])
		assert.NotEmpty(t, attrs["args_hash"])
		assert.Equal(t, float64(len(call.result)), attrs["result_chars"])
		assert.NotContains(t, attrs, "args_preview")
		assert.NotContains(t, attrs, "result_preview")
	}

	generic := calls[len(calls)-1]
	genericAttrs := persistedAgentV3ToolSpanAttrs(t, persisted.Spans, generic.name)
	assert.Equal(t, generic.args, genericAttrs["args_preview"])
	assert.Equal(t, generic.result, genericAttrs["result_preview"])
	assert.NotEmpty(t, genericAttrs["args_hash"])
	assert.Equal(t, float64(len(generic.result)), genericAttrs["result_chars"])

	summary, err := orm.AgentV3GetTraceSummary(t.Context(), scope)
	require.NoError(t, err)
	require.NotNil(t, summary)
	summaryPayload, err := json.Marshal(summary)
	require.NoError(t, err)
	for _, marker := range []string{
		"fixture-skill-body",
		"fixture-private-query",
		"fixture-private-result",
		"fixture-secret-token",
		"https://search.fixture.invalid/",
	} {
		assert.NotContains(t, string(summaryPayload), marker)
	}
}

func persistedAgentV3ToolSpanAttrs(t *testing.T, spans []AgentV3TraceSpan, toolName string) map[string]any {
	t.Helper()
	for _, span := range spans {
		if span.Name == "tool_call" && span.Attrs["tool"] == toolName {
			return span.Attrs
		}
	}
	t.Fatalf("missing persisted tool trace span for %q", toolName)
	return nil
}
