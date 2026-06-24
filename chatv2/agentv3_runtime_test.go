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

	"csust-got/chat"
	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestBuildAgentV3ToolsExposeOnlyFiveRuntimeTools(t *testing.T) {
	tools := buildAgentV3Tools(nonRichAgentV3ChatConfig(), &config.AgentV3Config{})
	require.Len(t, tools, 5)

	names := make([]string, 0, len(tools))
	for _, item := range tools {
		info, err := item.Info(t.Context())
		require.NoError(t, err)
		names = append(names, info.Name)
	}
	assert.Equal(t, []string{"read", "grep", "write", "edit", "bash"}, names)
	assert.NotContains(t, names, "load_skill")

	toolDefsText := agentV3ToolDefinitionsText(false)
	assert.NotContains(t, toolDefsText, "load_skill")
	assert.NotContains(t, toolDefsText, "/skills")
	assert.Contains(t, toolDefsText, "curl")
	assert.Contains(t, toolDefsText, "jq")
	assert.Contains(t, agentV3RuntimeSkillRules(), "curl")
	assert.Contains(t, agentV3RuntimeSkillRules(), "jq")
}

func nonRichAgentV3ChatConfig() *config.ChatConfigSingle {
	return &config.ChatConfigSingle{Agent: &config.AgentConfig{Enable: true, V3: true}}
}

func richAgentV3ChatConfig() *config.ChatConfigSingle {
	return &config.ChatConfigSingle{Agent: &config.AgentConfig{Enable: true, V3: true, Rich: true}}
}

func TestBuildAgentV3ToolsAddsLoadSkillOnlyForRich(t *testing.T) {
	richTools := buildAgentV3Tools(richAgentV3ChatConfig(), &config.AgentV3Config{})
	richNames := make([]string, 0, len(richTools))
	for _, item := range richTools {
		info, err := item.Info(t.Context())
		require.NoError(t, err)
		richNames = append(richNames, info.Name)
	}
	assert.ElementsMatch(t, []string{"read", "grep", "write", "edit", "bash", "load_skill"}, richNames)
	assert.Contains(t, agentV3ToolDefinitionsText(true), "load_skill")

	disabled := false
	noBuiltinTools := buildAgentV3Tools(richAgentV3ChatConfig(), &config.AgentV3Config{
		Skills: config.AgentV3SkillsConfig{InjectBuiltin: &disabled},
	})
	noBuiltinNames := make([]string, 0, len(noBuiltinTools))
	for _, item := range noBuiltinTools {
		info, err := item.Info(t.Context())
		require.NoError(t, err)
		noBuiltinNames = append(noBuiltinNames, info.Name)
	}
	assert.NotContains(t, noBuiltinNames, "load_skill")
}

func TestRemoteBashToolDocumentsCommonUtilities(t *testing.T) {
	info, err := (&remoteBashTool{}).Info(t.Context())
	require.NoError(t, err)

	assert.Contains(t, info.Desc, "curl")
	assert.Contains(t, info.Desc, "jq")
}

func TestLoadSkillToolLoadsOnlyAvailableRichSkill(t *testing.T) {
	old := config.BotConfig
	defer func() { config.BotConfig = old }()
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}

	tc := &TurnContext{Config: richAgentV3ChatConfig()}
	ctx := WithTurnContext(t.Context(), tc)

	out, err := (&loadSkillTool{}).InvokableRun(ctx, `{"name":"rich-message"}`)
	require.NoError(t, err)
	assert.Contains(t, out, "<loaded_skill name=\"rich-message\">")
	assert.Contains(t, out, "telegram_rich_message")

	tc.Config = nonRichAgentV3ChatConfig()
	out, err = (&loadSkillTool{}).InvokableRun(ctx, `{"name":"rich-message"}`)
	require.NoError(t, err)
	assert.Contains(t, out, "[Skill Error]")
}

func TestRichMessageAuthorizationRequiresImmediatelyPreviousLoadSkill(t *testing.T) {
	old := config.BotConfig
	defer func() { config.BotConfig = old }()
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}

	tc := &TurnContext{Config: richAgentV3ChatConfig()}
	assert.False(t, tc.richMessageSkillLoadedForFinal())

	seq := tc.recordToolCall(agentV3ToolLoadSkill)
	tc.markRichMessageSkillLoaded(seq)
	assert.True(t, tc.richMessageSkillLoadedForFinal())

	tc.recordToolCall(agentV3ToolRead)
	assert.False(t, tc.richMessageSkillLoadedForFinal())
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

func TestAgentV3TurnMessagesKeepSingleSystemPrompt(t *testing.T) {
	messages := buildAgentV3TurnMessages(
		"stable prefix",
		"- user: old question\n- assistant: old answer",
		nil,
		[]orm.AgentV3Turn{
			{Role: string(schema.User), Content: "recent user"},
			{Role: string(schema.Assistant), Content: "recent assistant"},
		},
		schema.UserMessage("current user"),
	)

	require.Len(t, messages, 5)
	assert.Equal(t, 1, countRoleMessages(messages, schema.System))
	assert.Equal(t, schema.System, messages[0].Role)
	assert.Equal(t, schema.User, messages[1].Role)
	assert.Contains(t, messages[1].Content, "<conversation_summary>")
	assert.Contains(t, messages[1].Content, "context only")

	ctx := WithTurnContext(t.Context(), &TurnContext{V3: &AgentV3TurnState{}})
	withDirective := injectLoopDirectives(ctx, messages)
	assert.Equal(t, 1, countRoleMessages(withDirective, schema.System))
	assert.Contains(t, withDirective[0].Content, "stable prefix")
	assert.Contains(t, withDirective[0].Content, "工具调用纪律")
}

func TestSanitizeHistoryMergesMultipleSystemMessages(t *testing.T) {
	messages := sanitizeHistory([]*schema.Message{
		schema.SystemMessage("stable prefix"),
		schema.SystemMessage("<conversation_summary>summary</conversation_summary>"),
		schema.UserMessage("hello"),
	})

	require.Len(t, messages, 2)
	assert.Equal(t, schema.System, messages[0].Role)
	assert.Equal(t, 1, countRoleMessages(messages, schema.System))
	assert.Contains(t, messages[0].Content, "stable prefix")
	assert.Contains(t, messages[0].Content, "conversation_summary")
	assert.Equal(t, schema.User, messages[1].Role)
}

func countRoleMessages(messages []*schema.Message, role schema.RoleType) int {
	count := 0
	for _, msg := range messages {
		if msg != nil && msg.Role == role {
			count++
		}
	}
	return count
}

func TestBuildAgentV3ToolsExposeRuntimeTools(t *testing.T) {
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{
		Name:     "v3",
		Model:    &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("ok", nil)}}},
		Tools:    buildAgentV3Tools(nonRichAgentV3ChatConfig(), &config.AgentV3Config{}),
		MaxSteps: 4,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"read", "grep", "write", "edit", "bash"}, agent.toolNames)
	assert.NotContains(t, agent.toolNames, "update_progress")
	assert.NotContains(t, agent.toolNames, "load_skill")
}

func TestAgentV3ToolSurfacePreservesConfiguredTools(t *testing.T) {
	configuredTools, err := buildConfiguredAgentTools(t.Context(), "v3", &config.AgentConfig{
		Tools: []string{"update_progress"},
		Skills: []*config.SkillConfig{
			{
				Name:  "context",
				Tools: []string{"get_context"},
			},
		},
	}, nil)
	require.NoError(t, err)

	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{
		Name:     "v3",
		Model:    &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("ok", nil)}}},
		Tools:    append(buildAgentV3Tools(nonRichAgentV3ChatConfig(), &config.AgentV3Config{}), configuredTools...),
		MaxSteps: 4,
	})
	require.NoError(t, err)

	for _, name := range []string{"read", "grep", "write", "edit", "bash", "update_progress", "get_context"} {
		assert.Contains(t, agent.toolNames, name)
	}
	assert.NotContains(t, agent.toolNames, "load_skill")
}

func TestAgentV3StageMarkersDescribeRuntimeIntent(t *testing.T) {
	calls := []schema.ToolCall{
		{
			Function: schema.FunctionCall{
				Name:      "read",
				Arguments: `{"path":"/workspace/notes.md"}`,
			},
		},
		{
			Function: schema.FunctionCall{
				Name:      "bash",
				Arguments: `{"command":"go test ./chatv2"}`,
			},
		},
		{
			Function: schema.FunctionCall{
				Name:      "edit",
				Arguments: `{"path":"/workspace/a.txt"}`,
			},
		},
		{
			Function: schema.FunctionCall{
				Name:      "load_skill",
				Arguments: `{"name":"rich-message"}`,
			},
		},
	}

	got := buildAgentV3StageMarker(calls)
	assert.Contains(t, got, "正在读取 runtime 文件")
	assert.Contains(t, got, "$ go test ./chatv2")
	assert.Contains(t, got, "正在编辑 runtime 文件")
	assert.Contains(t, got, "正在加载 skill: rich-message")
	assert.NotContains(t, got, "skill 文档")
	assert.NotContains(t, got, "skill CLI")
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

func TestRenderAgentV3SoulIncludesSkillPromptAddons(t *testing.T) {
	old := config.BotConfig
	defer func() { config.BotConfig = old }()
	config.BotConfig = nil

	cc := &CompiledChat{
		SystemTemplate:    template.Must(template.New("system").Parse("static soul for {{ .BotUsername }}")),
		SkillPromptAddons: "skill addon",
	}
	got, err := renderAgentV3Soul(cc, &TurnContext{BotUser: &tb.User{Username: "bot"}})
	require.NoError(t, err)
	assert.Equal(t, "static soul for bot\n\nskill addon", got)
}

func TestRenderAgentV3SoulPathOverridesDynamicSystemPrompt(t *testing.T) {
	old := config.BotConfig
	defer func() { config.BotConfig = old }()
	soulPath := filepath.Join(t.TempDir(), "soul.md")
	require.NoError(t, os.WriteFile(soulPath, []byte("static soul"), 0o644))
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{SoulPath: soulPath}}

	cc := &CompiledChat{
		SystemTemplate:    template.Must(template.New("system").Parse("now {{ .CurrentDateCN }} {{ .Input }}")),
		SkillPromptAddons: "skill addon",
	}
	got, err := renderAgentV3Soul(cc, &TurnContext{})
	require.NoError(t, err)
	assert.Equal(t, "static soul\n\nskill addon", got)
}

func TestAgentV3UserMessageRendersPromptWithHistoryContext(t *testing.T) {
	replyTo := 10
	history := &RichHistory{ContextMessages: []*chat.ContextMessage{
		{ID: 10, User: "alice", Text: "earlier context"},
		{ID: 11, ReplyTo: &replyTo, User: "bob", Text: "reply context"},
	}}
	cc := &CompiledChat{
		PromptTemplate: template.Must(template.New("prompt").Parse("ctx={{ .ContextXml }}\ninput={{ .Input }}")),
	}
	tc := &TurnContext{
		Message: &tb.Message{Text: "current input"},
		BotUser: &tb.User{Username: "bot"},
	}

	msg, err := buildAgentV3UserMessage(cc, tc, history)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, schema.User, msg.Role)
	assert.Contains(t, msg.Content, "<dynamic_suffix>")
	assert.Contains(t, msg.Content, "<messages>")
	assert.Contains(t, msg.Content, "earlier context")
	assert.Contains(t, msg.Content, `replyTo="10"`)
	assert.Contains(t, msg.Content, "input=current input")
}

func TestAgentV3FallbackHistoryMessagesOnlyBeforeRawTurns(t *testing.T) {
	history := &RichHistory{ContextMessages: []*chat.ContextMessage{
		{ID: 1, User: "alice", Text: "hello"},
	}}
	tc := &TurnContext{BotUser: &tb.User{Username: "bot"}}

	fallback := agentV3FallbackHistoryMessages(nil, history, tc)
	require.Len(t, fallback, 1)
	assert.Equal(t, schema.User, fallback[0].Role)
	assert.Contains(t, fallback[0].Content, "[alice]: hello")

	assert.Empty(t, agentV3FallbackHistoryMessages([]orm.AgentV3Turn{{Role: string(schema.User), Content: "raw"}}, history, tc))
	assert.Empty(t, agentV3FallbackHistoryMessages(nil, &RichHistory{}, tc))
}

func TestPrepareAgentV3TurnLeavesTraceOnContextBuildError(t *testing.T) {
	old := config.BotConfig
	defer func() { config.BotConfig = old }()
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{
		SoulPath: filepath.Join(t.TempDir(), "missing-soul.md"),
		Runtime:  config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
		Skills:   config.AgentV3SkillsConfig{Mode: "system_prompt"},
	}}
	tc := &TurnContext{
		Message: &tb.Message{ID: 42},
		ChatID:  -100,
		Config:  &config.ChatConfigSingle{Model: &config.Model{Model: "test-model"}},
		BotUser: &tb.User{Username: "bot"},
	}

	_, err := prepareAgentV3Turn(t.Context(), &CompiledChat{Name: "agent"}, tc, nil)
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
		Skills:  config.AgentV3SkillsConfig{Mode: "system_prompt"},
	}))
	assert.ErrorContains(t, validateAgentV3RuntimeConfig(&config.AgentV3Config{
		Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
		Skills:  config.AgentV3SkillsConfig{Mode: "runtime_filesystem"},
	}), "expected system_prompt")
	assert.ErrorContains(t, validateAgentV3RuntimeConfig(&config.AgentV3Config{
		Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
		Skills:  config.AgentV3SkillsConfig{Mode: "system_prompt", Root: "/skills"},
	}), "expected empty")
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

func TestBuildAgentV3BuiltinSkillsRespectInjectionGate(t *testing.T) {
	old := config.BotConfig
	defer func() { config.BotConfig = old }()
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}

	tc := &TurnContext{Config: &config.ChatConfigSingle{Agent: &config.AgentConfig{Enable: true, V3: true, Rich: true}}}
	cfg := &config.AgentV3Config{}
	require.Len(t, buildAgentV3BuiltinSkills(tc, cfg), 1)

	disabled := false
	cfg.Skills.InjectBuiltin = &disabled
	assert.Empty(t, buildAgentV3BuiltinSkills(tc, cfg))
}

func TestBuildAgentV3StablePrefixIncludesSkillPromptBlockOnlyWhenProvided(t *testing.T) {
	withoutRich := buildAgentV3StablePrefix("soul", "memory", "tools", "")
	assert.NotContains(t, withoutRich, "<rich_message_skill>")
	assert.NotContains(t, withoutRich, "\n<agent_v3_skills>\n")
	assert.Contains(t, withoutRich, "<tool_definitions>")

	skillBlock := buildAgentV3SkillPromptBlock([]agentV3BuiltinSkill{{
		Name:        "rich-message",
		Description: "Rich output",
		Content:     "rich rules",
	}})
	withRich := buildAgentV3StablePrefix("soul", "memory", "tools", skillBlock)
	assert.NotContains(t, withRich, "<rich_message_skill>")
	assert.Contains(t, withRich, "<agent_v3_skills>")
	assert.Contains(t, withRich, "Do not use read/grep to load skills from /skills")
	assert.Contains(t, withRich, "load_skill")
	assert.Contains(t, withRich, "<skill name=\"rich-message\" description=\"Rich output\" status=\"available\" />")
	assert.NotContains(t, withRich, "rich rules")
	assert.Contains(t, withRich, "<tool_definitions>")

	idxRuntimeRules := strings.Index(withRich, "<runtime_and_skill_rules>")
	idxSkills := strings.Index(withRich, "<agent_v3_skills>")
	idxToolDefs := strings.Index(withRich, "<tool_definitions>")
	assert.Greater(t, idxRuntimeRules, -1, "<runtime_and_skill_rules> must be present")
	assert.Less(t, idxRuntimeRules, idxSkills, "<runtime_and_skill_rules> must appear before <agent_v3_skills>")
	assert.Less(t, idxSkills, idxToolDefs, "<agent_v3_skills> must appear before <tool_definitions>")
}

func TestBuildAgentV3PrefixHashSeparatesSkillPromptBlock(t *testing.T) {
	soulHash := hashString("soul")
	memoryHash := hashString("memory")
	toolDefsHash := hashString("tools")
	withoutRich := buildAgentV3PrefixHash(soulHash, memoryHash, toolDefsHash, hashString(""))
	withRich := buildAgentV3PrefixHash(soulHash, memoryHash, toolDefsHash, hashString(buildAgentV3SkillPromptBlock([]agentV3BuiltinSkill{{Name: "rich-message", Content: agentV3RichMessageSkillContract(true)}})))

	assert.NotEqual(t, withoutRich, withRich)
	assert.Equal(t, withoutRich, buildAgentV3PrefixHash(soulHash, memoryHash, toolDefsHash, hashString("")))
}
