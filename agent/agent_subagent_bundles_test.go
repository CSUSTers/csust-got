package agentv3

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"csust-got/config"

	"github.com/cloudwego/eino/components/tool"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestSubAgentSkillBundleMergePreservesOrderPrecedenceAndConfig(t *testing.T) {
	directModel := &config.Model{Model: "direct"}
	firstModel := &config.Model{Model: "first"}
	lastModel := &config.Model{Model: "last"}
	directMCP := &config.ToolServerConfig{Url: "direct"}
	firstMCP := &config.ToolServerConfig{Url: "first"}
	lastMCP := &config.ToolServerConfig{Url: "last"}
	cfg := &config.SubAgentConfig{
		Tools:      []string{"get_message", "get_message"},
		McpServers: []*config.ToolServerConfig{directMCP},
		ToolModels: map[string]*config.Model{"analyze_image": directModel},
		SkillBundles: []*config.SkillConfig{
			nil,
			{Tools: []string{"get_message", "analyze_image", "analyze_image"}, McpServers: []*config.ToolServerConfig{firstMCP}, ToolModels: map[string]*config.Model{"analyze_image": firstModel, "other": firstModel}},
			{Tools: []string{"analyze_image", "get_context"}, McpServers: []*config.ToolServerConfig{lastMCP}, ToolModels: map[string]*config.Model{"other": lastModel}, SystemPromptAddon: "addon"},
		},
	}
	before, err := json.Marshal(cfg)
	require.NoError(t, err)
	options := &config.AgentOptions{Tools: cfg.Tools, McpServers: cfg.McpServers, ToolModels: cfg.ToolModels, Skills: cfg.SkillBundles}
	tools, servers, models := mergeSkillConfigs(options)
	require.Equal(t, []string{"get_message", "get_message", "analyze_image", "get_context"}, tools)
	require.Equal(t, []*config.ToolServerConfig{directMCP, firstMCP, lastMCP}, servers)
	require.Same(t, directModel, models["analyze_image"])
	require.Same(t, firstModel, models["other"])
	require.Equal(t, "addon", GetSkillPromptAddons(options))
	after, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	models["other"] = lastModel
	tools[0] = "changed"
	servers[0] = lastMCP
	require.Equal(t, "get_message", cfg.Tools[0])
	require.Same(t, directMCP, cfg.McpServers[0])
	require.NotContains(t, cfg.ToolModels, "other")
}

func TestSubAgentSkillBundlesInvokeVisionWithEffectiveModelAndPrompt(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit_override_%t", explicit), func(t *testing.T) {
			var mu sync.Mutex
			var visionModels []string
			vision := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string `json:"model"`
					Messages []any  `json:"messages"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Contains(t, fmt.Sprint(body.Messages), "data:image/jpeg;base64,")
				mu.Lock()
				visionModels = append(visionModels, body.Model)
				mu.Unlock()
				writeFakeOpenAIResponse(w, false, map[string]any{"role": "assistant", "content": "VISION_RESULT"}, "stop")
			}))
			t.Cleanup(vision.Close)
			telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/getFile") {
					_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"photo.jpg"}}`))
					return
				}
				require.Equal(t, "/file/botfixture-token/photo.jpg", r.URL.Path)
				_, _ = w.Write([]byte("fixture-image"))
			}))
			t.Cleanup(telegram.Close)
			bot, err := tb.NewBot(tb.Settings{Token: "fixture-token", URL: telegram.URL, Offline: true})
			require.NoError(t, err)
			var captured []fakeOpenAIRequest
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body fakeOpenAIRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				mu.Lock()
				captured = append(captured, body)
				mu.Unlock()
				for _, message := range body.Messages {
					if message.Role == "tool" {
						require.Equal(t, "VISION_RESULT", message.Content)
						writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "content": "done"}, "stop")
						return
					}
				}
				writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "tool_calls": []any{
					map[string]any{"index": 0, "id": "vision-call", "type": "function", "function": map[string]any{"name": "analyze_image", "arguments": `{"file_id":"fixture-file","query":"describe"}`}},
				}}, "tool_calls")
			}))
			t.Cleanup(model.Close)
			first := &config.Model{Model: "bundle-first", BaseUrl: vision.URL, ApiKey: "fixture"}
			cfg := &config.SubAgentConfig{
				Name: "vision_child", Description: "inspect images", Model: &config.Model{Model: "child", BaseUrl: model.URL, ApiKey: "fixture"},
				Skills: []string{"alpha"}, MaxSteps: 1,
				SkillBundles: []*config.SkillConfig{nil,
					{Name: "first", Tools: []string{"analyze_image"}, ToolModels: map[string]*config.Model{"analyze_image": first}, SystemPromptAddon: "FIRST_ADDON"},
					{Name: "second", Tools: []string{"analyze_image"}, ToolModels: map[string]*config.Model{"analyze_image": {Model: "bundle-last", BaseUrl: vision.URL}}, SystemPromptAddon: "SECOND_ADDON"},
				},
			}
			wantModel := "bundle-first"
			wantPrompt := "You are vision_child. inspect images"
			if explicit {
				cfg.ToolModels = map[string]*config.Model{"analyze_image": {Model: "direct", BaseUrl: vision.URL, ApiKey: "fixture"}}
				cfg.SystemPrompt = "EXPLICIT_PROMPT"
				wantModel, wantPrompt = "direct", "EXPLICIT_PROMPT"
			}
			before, err := json.Marshal(cfg)
			require.NoError(t, err)
			parentCfg := &config.AgentConfig{Model: &config.Model{Model: "parent-only"}, Agent: &config.AgentOptions{Tools: []string{"get_context"}, Rich: true}}
			ctx, cancel := context.WithTimeout(WithTurnContext(t.Context(), &TurnContext{Bot: bot, Config: parentCfg}), 10*time.Second)
			defer cancel()
			sub, err := buildSubAgentTool(ctx, cfg, nil)
			require.NoError(t, err)
			out, err := sub.(tool.InvokableTool).InvokableRun(ctx, `{"request":"inspect"}`)
			require.NoError(t, err)
			require.Equal(t, "done", out)
			after, err := json.Marshal(cfg)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, []string{wantModel}, visionModels)
			require.Len(t, captured, 2)
			names := make([]string, 0, len(captured[0].Tools))
			for _, item := range captured[0].Tools {
				names = append(names, item.Function.Name)
			}
			require.ElementsMatch(t, []string{"analyze_image", "load_skill"}, names)
			var system strings.Builder
			for _, message := range captured[0].Messages {
				if message.Role == "system" {
					_, _ = fmt.Fprint(&system, message.Content)
				}
			}
			renderedSystem := system.String()
			require.Contains(t, renderedSystem, wantPrompt)
			firstIndex, secondIndex, skillsIndex := strings.Index(renderedSystem, "FIRST_ADDON"), strings.Index(renderedSystem, "SECOND_ADDON"), strings.Index(renderedSystem, "<subagent_skills>")
			require.Greater(t, firstIndex, strings.Index(renderedSystem, wantPrompt))
			require.Greater(t, secondIndex, firstIndex)
			require.Greater(t, skillsIndex, secondIndex)
		})
	}
}

func TestSubAgentSkillBundleToolsClampActualADKSteps(t *testing.T) {
	var mu sync.Mutex
	var rounds int
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body fakeOpenAIRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		rounds++
		current := rounds
		mu.Unlock()
		if current == 4 {
			writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "content": "fourth round complete"}, "stop")
			return
		}
		writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"index": 0, "id": fmt.Sprintf("call-%d", current), "type": "function", "function": map[string]any{"name": "analyze_image", "arguments": `{}`}},
		}}, "tool_calls")
	}))
	t.Cleanup(model.Close)
	cfg := &config.SubAgentConfig{Name: "steps", MaxSteps: 1, Model: &config.Model{Model: "fixture", BaseUrl: model.URL}, SkillBundles: []*config.SkillConfig{{Tools: []string{"analyze_image"}}}}
	require.True(t, subAgentHasTools(cfg))
	ctx, cancel := context.WithTimeout(WithTurnContext(t.Context(), &TurnContext{}), 10*time.Second)
	defer cancel()
	sub, err := buildSubAgentTool(ctx, cfg, nil)
	require.NoError(t, err)
	out, err := sub.(tool.InvokableTool).InvokableRun(ctx, `{"request":"inspect"}`)
	require.NoError(t, err)
	require.Equal(t, "fourth round complete", out)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 4, rounds)
	for _, bundle := range []*config.SkillConfig{nil, {SystemPromptAddon: "addon"}, {ToolModels: map[string]*config.Model{"analyze_image": {Model: "vision"}}}} {
		require.False(t, subAgentHasTools(&config.SubAgentConfig{SkillBundles: []*config.SkillConfig{bundle}}))
	}
	require.True(t, subAgentHasTools(&config.SubAgentConfig{SkillBundles: []*config.SkillConfig{{McpServers: []*config.ToolServerConfig{{Enable: true}}}}}))
}

func TestSubAgentSkillBundleBuildErrorPolicies(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body fakeOpenAIRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Empty(t, body.Tools)
		writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "content": "without MCP"}, "stop")
	}))
	t.Cleanup(model.Close)
	cfg := &config.SubAgentConfig{Name: "bad", Model: &config.Model{Model: "fixture", BaseUrl: model.URL}, SkillBundles: []*config.SkillConfig{{Tools: []string{"unknown-builtin"}}}}
	_, err := buildSubAgentTool(t.Context(), cfg, nil)
	require.ErrorIs(t, err, errUnknownTool)
	parentTools, err := buildConfiguredAgentTools(t.Context(), "parent", &config.AgentOptions{Tools: []string{"get_message"}, SubAgents: []*config.SubAgentConfig{cfg}}, nil)
	require.NoError(t, err)
	require.Len(t, parentTools, 1)
	info, err := parentTools[0].Info(t.Context())
	require.NoError(t, err)
	require.Equal(t, "get_message", info.Name)
	brokenMCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not-openapi")) }))
	t.Cleanup(brokenMCP.Close)
	cfg.SkillBundles = []*config.SkillConfig{{McpServers: []*config.ToolServerConfig{{Enable: true, Type: config.ToolServerTypeMCPO, Url: brokenMCP.URL, Tools: config.ToolEntries{{Name: "broken"}}}}}}
	sub, err := buildSubAgentTool(t.Context(), cfg, NewMcpManager())
	require.NoError(t, err)
	out, err := sub.(tool.InvokableTool).InvokableRun(t.Context(), `{"request":"continue"}`)
	require.NoError(t, err)
	require.Equal(t, "without MCP", out)
}

func TestSubAgentSkillBundleMCPToolsReachModelAndExecute(t *testing.T) {
	var mu sync.Mutex
	var executions int
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clock/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","info":{"title":"clock","version":"1"},"paths":{"/bundle_clock":{"post":{"description":"Read clock","requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{}}}}},"responses":{"200":{"description":"OK"}}}}}}`))
		case "/clock/bundle_clock":
			require.Equal(t, http.MethodPost, r.Method)
			mu.Lock()
			executions++
			mu.Unlock()
			_, _ = w.Write([]byte("BUNDLE_CLOCK_RESULT"))
		default:
			t.Errorf("unexpected MCP path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(mcp.Close)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body fakeOpenAIRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Len(t, body.Tools, 1)
		require.Equal(t, "bundle_clock", body.Tools[0].Function.Name)
		for _, message := range body.Messages {
			if message.Role == "tool" {
				require.Equal(t, "BUNDLE_CLOCK_RESULT", message.Content)
				writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "content": "clock complete"}, "stop")
				return
			}
		}
		writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"index": 0, "id": "clock-call", "type": "function", "function": map[string]any{"name": "bundle_clock", "arguments": `{}`}},
		}}, "tool_calls")
	}))
	t.Cleanup(model.Close)
	cfg := &config.SubAgentConfig{
		Name: "mcp_child", Model: &config.Model{Model: "fixture", BaseUrl: model.URL}, MaxSteps: 1,
		SkillBundles: []*config.SkillConfig{{McpServers: []*config.ToolServerConfig{{Enable: true, Type: config.ToolServerTypeMCPO, Url: mcp.URL, Tools: config.ToolEntries{{Name: "clock"}}}}}},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	sub, err := buildSubAgentTool(ctx, cfg, NewMcpManager())
	require.NoError(t, err)
	out, err := sub.(tool.InvokableTool).InvokableRun(ctx, `{"request":"read clock"}`)
	require.NoError(t, err)
	require.Equal(t, "clock complete", out)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, executions)
}
