//go:build !386 && !arm

package chatv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"
	"unicode/utf8"

	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestBuildAgentV3ToolsExposeOnlyFiveRuntimeTools(t *testing.T) {
	tools := buildAgentV3Tools()
	require.Len(t, tools, 5)

	names := make([]string, 0, len(tools))
	for _, item := range tools {
		info, err := item.Info(t.Context())
		require.NoError(t, err)
		names = append(names, info.Name)
	}
	assert.Equal(t, []string{"read", "grep", "write", "edit", "bash"}, names)
}

func TestRemoteRuntimeClientAndBashTool(t *testing.T) {
	var gotAuth string
	var gotReq runtimeBashRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		assert.Equal(t, "/v1/bash", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotReq))
		_ = json.NewEncoder(w).Encode(runtimeBashResponse{
			ExitCode:   0,
			Stdout:     "hello",
			DurationMS: 7,
		})
	}))
	defer srv.Close()

	client := &RemoteRuntimeClient{
		Endpoint:       srv.URL,
		AuthToken:      "secret",
		HTTPClient:     srv.Client(),
		CommandTimeout: time.Second,
		MaxOutputChars: 100,
	}
	trace := NewAgentV3Trace("run_test", -100, 42)
	tc := &TurnContext{
		RunID:         "run_test",
		Namespace:     "bot:tg:-100",
		RuntimeClient: client,
		V3:            &AgentV3TurnState{Trace: trace},
	}
	ctx := WithTurnContext(t.Context(), tc)

	out, err := (&remoteBashTool{}).InvokableRun(ctx, `{"command":"echo hello","cwd":"/workspace","timeout":"1s"}`)
	require.NoError(t, err)
	assert.Contains(t, out, "exit_code: 0")
	assert.Contains(t, out, "stdout:\nhello")
	assert.Equal(t, "Bearer secret", gotAuth)
	assert.Equal(t, "bot:tg:-100", gotReq.Namespace)
	assert.Equal(t, "run_test", gotReq.RunID)
	assert.NotNil(t, trace.LastBashExitCode)
	assert.Equal(t, 7, int(trace.LastBashDurationMS))
}

func TestRemoteRuntimeClientReset(t *testing.T) {
	var gotReq runtimeResetRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/reset", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotReq))
		_ = json.NewEncoder(w).Encode(runtimeResetResponse{
			OK:            true,
			NamespaceHash: "hash",
			Removed:       true,
		})
	}))
	defer srv.Close()

	client := &RemoteRuntimeClient{
		Endpoint:       srv.URL,
		HTTPClient:     srv.Client(),
		CommandTimeout: time.Second,
		MaxOutputChars: 100,
	}
	resp, err := client.Reset(t.Context(), runtimeResetRequest{
		runtimeCommonRequest: runtimeCommonRequest{
			Namespace: "bot:tg:-100",
			RunID:     "run_reset",
		},
	})
	require.NoError(t, err)
	assert.True(t, resp.OK)
	assert.True(t, resp.Removed)
	assert.Equal(t, "bot:tg:-100", gotReq.Namespace)
	assert.Equal(t, "run_reset", gotReq.RunID)
}

func TestAgentV3Helpers(t *testing.T) {
	assert.Equal(t, "fact", extractExplicitMemoryContent("记住：fact"))
	assert.Empty(t, extractExplicitMemoryContent("普通聊天"))

	turns := agentV3TurnsToMessages([]orm.AgentV3Turn{
		{Role: string(schema.User), Content: "u"},
		{Role: string(schema.Assistant), Content: "a"},
	})
	require.Len(t, turns, 2)
	assert.Equal(t, schema.User, turns[0].Role)
	assert.Equal(t, schema.Assistant, turns[1].Role)

	summary := summarizeAgentV3Turns([]orm.AgentV3Turn{
		{Role: string(schema.User), Content: "  hello\nworld  "},
		{Role: string(schema.Assistant), Content: "answer"},
	}, 100)
	assert.Contains(t, summary, "- user: hello world")
	assert.Contains(t, summary, "- assistant: answer")

	trimmed := trimAgentV3TurnsByMaxChars([]orm.AgentV3Turn{
		{Content: "old"},
		{Content: "middle"},
		{Content: "new"},
	}, 9)
	require.Len(t, trimmed, 2)
	assert.Equal(t, "middle", trimmed[0].Content)
}

func TestCompileAgentV3DoesNotExposeLegacyTools(t *testing.T) {
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{
		Name:     "v3",
		Model:    &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("ok", nil)}}},
		Tools:    buildAgentV3Tools(),
		MaxSteps: 4,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"read", "grep", "write", "edit", "bash"}, agent.toolNames)
	assert.NotContains(t, agent.toolNames, "update_progress")
}

func TestAgentV3StageMarkersDescribeRuntimeIntent(t *testing.T) {
	calls := []schema.ToolCall{
		{
			Function: schema.FunctionCall{
				Name:      "read",
				Arguments: `{"path":"/skills/web-research/SKILL.md"}`,
			},
		},
		{
			Function: schema.FunctionCall{
				Name:      "bash",
				Arguments: `{"command":"bash /skills/web-research/scripts/search.sh \"cache\""}`,
			},
		},
		{
			Function: schema.FunctionCall{
				Name:      "edit",
				Arguments: `{"path":"/workspace/a.txt"}`,
			},
		},
	}

	got := buildAgentV3StageMarker(calls)
	assert.Contains(t, got, "正在读取 skill 文档")
	assert.Contains(t, got, "正在执行 skill CLI")
	assert.Contains(t, got, "正在编辑 runtime 文件")
}

func TestAgentV3RuntimeTruncateKeepsUTF8(t *testing.T) {
	got, truncated := truncateForModel("你好hello", 7, false)
	require.True(t, truncated)
	assert.True(t, utf8.ValidString(got))
	assert.Contains(t, got, "[truncated by bot]")
}

func TestRenderAgentV3SoulRejectsDynamicSystemPrompt(t *testing.T) {
	old := config.BotConfig
	defer func() { config.BotConfig = old }()
	config.BotConfig = nil

	cc := &CompiledChat{
		SystemTemplate: template.Must(template.New("system").Parse("now {{ .CurrentDateCN }} {{ .Input }}")),
	}
	_, err := renderAgentV3Soul(cc, &TurnContext{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dynamic field")
}

func TestRenderAgentV3SoulPathOverridesDynamicSystemPrompt(t *testing.T) {
	old := config.BotConfig
	defer func() { config.BotConfig = old }()
	soulPath := filepath.Join(t.TempDir(), "soul.md")
	require.NoError(t, os.WriteFile(soulPath, []byte("static soul"), 0o644))
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{SoulPath: soulPath}}

	cc := &CompiledChat{
		SystemTemplate: template.Must(template.New("system").Parse("now {{ .CurrentDateCN }} {{ .Input }}")),
	}
	got, err := renderAgentV3Soul(cc, &TurnContext{})
	require.NoError(t, err)
	assert.Equal(t, "static soul", got)
}

func TestPrepareAgentV3TurnLeavesTraceOnContextBuildError(t *testing.T) {
	old := config.BotConfig
	defer func() { config.BotConfig = old }()
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{
		SoulPath: filepath.Join(t.TempDir(), "missing-soul.md"),
		Runtime:  config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
		Skills:   config.AgentV3SkillsConfig{Mode: "runtime_filesystem", Root: "/skills"},
	}}
	tc := &TurnContext{
		Message: &tb.Message{ID: 42},
		ChatID:  -100,
		Config:  &config.ChatConfigSingle{Model: &config.Model{Model: "test-model"}},
		BotUser: &tb.User{Username: "bot"},
	}

	_, err := prepareAgentV3Turn(t.Context(), &CompiledChat{Name: "agent"}, tc)
	require.Error(t, err)
	require.NotNil(t, tc.V3)
	require.NotNil(t, tc.V3.Trace)
	assert.Contains(t, tc.V3.Trace.Error, "soul_path")
	require.NotEmpty(t, tc.V3.Trace.Spans)
	assert.Equal(t, "context_build", tc.V3.Trace.Spans[0].Name)
	assert.Contains(t, tc.V3.Trace.Spans[0].Error, "soul_path")
}

func TestValidateAgentV3RuntimeConfig(t *testing.T) {
	assert.ErrorContains(t, validateAgentV3RuntimeConfig(&config.AgentV3Config{}), "runtime is disabled")
	assert.ErrorContains(t, validateAgentV3RuntimeConfig(&config.AgentV3Config{
		Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "host"},
	}), "unsupported")
	assert.NoError(t, validateAgentV3RuntimeConfig(&config.AgentV3Config{
		Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
		Skills:  config.AgentV3SkillsConfig{Mode: "runtime_filesystem", Root: "/skills"},
	}))
}

func TestAgentV3TracePreviewRespectsConfig(t *testing.T) {
	old := config.BotConfig
	defer func() { config.BotConfig = old }()
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{
		Observability: config.AgentV3ObservabilityConfig{
			CaptureContent: "preview",
			PreviewChars:   5,
		},
	}}

	preview, ok := agentV3TracePreview("hello world")
	require.True(t, ok)
	assert.Equal(t, "hello\n[truncated]", preview)
}

func TestAgentV3TraceSpanSummaryCompactsAttrs(t *testing.T) {
	spans := compactAgentV3TraceSpans([]AgentV3TraceSpan{
		{
			Name:       "context_cache",
			DurationMS: 12,
			Attrs: map[string]any{
				"cache_hit": true,
				"long":      strings.Repeat("x", 300),
			},
		},
		{
			Name:       "tool_call",
			DurationMS: 7,
			Error:      "failed",
			Attrs:      map[string]any{"tool": "bash"},
		},
	})
	require.Len(t, spans, 2)
	assert.Equal(t, "context_cache", spans[0].Name)
	assert.Equal(t, int64(12), spans[0].DurationMS)
	assert.Equal(t, true, spans[0].Attrs["cache_hit"])
	assert.Contains(t, spans[0].Attrs["long"], "[truncated]")
	assert.Equal(t, "failed", spans[1].Error)
	assert.Equal(t, "bash", spans[1].Attrs["tool"])
}

func TestAgentV3TraceUsageIsAccumulated(t *testing.T) {
	trace := NewAgentV3Trace("run_test", -100, 1)
	trace.RecordUsage(&schema.Message{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens: 10,
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens: 4,
		},
	}}})
	trace.RecordUsage(&schema.Message{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens: 7,
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens: 2,
		},
	}}})
	assert.Equal(t, 17, trace.PromptTokens)
	assert.Equal(t, 6, trace.CachedTokens)
}

func TestAgentV3RichMessageRulesAreGated(t *testing.T) {
	assert.Empty(t, agentV3RichMessageSkillContract(false))

	enabled := agentV3RichMessageSkillContract(true)
	assert.Contains(t, enabled, "telegram_rich_message")
	assert.Contains(t, enabled, "raw Telegram Rich Markdown")
	assert.Contains(t, enabled, "not JSON")
	assert.Contains(t, enabled, "Do not emit mode fields")
	assert.Contains(t, enabled, "headings, lists, task lists")
}

func TestBuildAgentV3StablePrefixIncludesRichContractOnlyWhenProvided(t *testing.T) {
	withoutRich := buildAgentV3StablePrefix("soul", "memory", "tools", "")
	assert.NotContains(t, withoutRich, "<rich_message_skill>")
	assert.Contains(t, withoutRich, "<tool_definitions>")

	withRich := buildAgentV3StablePrefix("soul", "memory", "tools", "rich rules")
	assert.Contains(t, withRich, "<rich_message_skill>\nrich rules\n</rich_message_skill>")
	assert.Contains(t, withRich, "<tool_definitions>")
	assert.Less(t, strings.Index(withRich, "<rich_message_skill>"), strings.Index(withRich, "<tool_definitions>"))
}

func TestBuildAgentV3PrefixHashSeparatesRichGate(t *testing.T) {
	soulHash := hashString("soul")
	memoryHash := hashString("memory")
	toolDefsHash := hashString("tools")
	withoutRich := buildAgentV3PrefixHash(soulHash, memoryHash, toolDefsHash, hashString(""))
	withRich := buildAgentV3PrefixHash(soulHash, memoryHash, toolDefsHash, hashString(agentV3RichMessageSkillContract(true)))

	assert.NotEqual(t, withoutRich, withRich)
	assert.Equal(t, withoutRich, buildAgentV3PrefixHash(soulHash, memoryHash, toolDefsHash, hashString("")))
}
