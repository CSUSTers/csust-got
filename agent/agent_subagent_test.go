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
)

func TestCapSubAgentResult(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{name: "unlimited", in: strings.Repeat("x", 50), limit: 0, want: strings.Repeat("x", 50)},
		{name: "fits", in: "short", limit: 10, want: "short"},
		{name: "head and tail kept", in: "0123456789abcdef", limit: 8, want: "0123\n[subagent result truncated: 8 chars omitted]\ncdef"},
		{name: "utf8 boundary", in: strings.Repeat("好", 10), limit: 7, want: "好\n[subagent result truncated: 24 chars omitted]\n好"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, capSubAgentResult(tt.in, tt.limit))
		})
	}
}

type fakeOpenAIRequest struct {
	Stream   bool `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

func writeFakeOpenAIResponse(w http.ResponseWriter, stream bool, message map[string]any, finish string) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		data, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": nil}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", finish)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
}

func TestSubAgentRuntimeToolsAndFilteredSkills(t *testing.T) {
	old := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	t.Cleanup(func() { config.BotConfig = old })

	var mu sync.Mutex
	var toolResults []string
	var toolNames []string
	var runtimeNamespace, runtimeRunID string
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/read", r.URL.Path)
		var req runtimeReadRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		runtimeNamespace, runtimeRunID = req.Namespace, req.RunID
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(runtimeTextResponse{Content: "NOTES_CONTENT " + req.Path})
	}))
	t.Cleanup(runtime.Close)

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body fakeOpenAIRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		if len(toolNames) == 0 {
			for _, item := range body.Tools {
				toolNames = append(toolNames, item.Function.Name)
			}
		}
		sawTool := false
		for _, message := range body.Messages {
			if message.Role == "tool" {
				sawTool = true
				toolResults = append(toolResults, fmt.Sprint(message.Content))
			}
		}
		mu.Unlock()
		if sawTool {
			writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "content": strings.Repeat("R", 2000)}, "stop")
			return
		}
		calls := []any{}
		for i, call := range []struct{ name, args string }{
			{"load_skill", `{"name":"searxng"}`},
			{"load_skill", `{"name":"rich-message"}`},
			{"read", `{"path":"/workspace/notes.txt"}`},
		} {
			calls = append(calls, map[string]any{"index": i, "id": fmt.Sprintf("call-%d", i), "type": "function", "function": map[string]any{"name": call.name, "arguments": call.args}})
		}
		writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "content": "", "tool_calls": calls}, "tool_calls")
	}))
	t.Cleanup(model.Close)

	catalog, _, err := mergeAgentV3SkillSnapshots(testSkillSnapshot(agentV3SkillSourceBotLocal, []agentV3SkillDescriptor{
		{Name: "searxng", Description: "Search the web.", Content: "# searxng\nSearch the web.\n\nSEARXNG_SKILL_BODY\n", VirtualPath: "/skills/searxng/SKILL.md"},
		{Name: "rich-message", Description: "Rich output.", Content: "# rich-message\nRich output.\n\nRICH_SKILL_BODY\n", VirtualPath: "/skills/rich-message/SKILL.md"},
	}))
	require.NoError(t, err)
	tc := &TurnContext{
		Config:        nonRichAgentV3ChatConfig(),
		RunID:         "run_sub",
		Namespace:     "bot:tg:-100",
		RuntimeClient: &RemoteRuntimeClient{Endpoint: runtime.URL, HTTPClient: runtime.Client(), CommandTimeout: time.Second, MaxOutputChars: 1000},
		V3:            &AgentV3TurnState{SkillCatalog: catalog, loadedSkillNames: map[string]struct{}{}},
	}
	ctx, cancel := context.WithTimeout(WithTurnContext(t.Context(), tc), 10*time.Second)
	defer cancel()

	sub, err := buildSubAgentTool(ctx, &config.SubAgentConfig{
		Name:           "web_researcher",
		Description:    "research",
		Model:          &config.Model{Name: "fixture", Model: "fixture", BaseUrl: model.URL, ApiKey: "fixture"},
		Runtime:        true,
		Skills:         []string{"searxng"},
		MaxResultChars: 300,
	}, nil)
	require.NoError(t, err)

	out, err := sub.(tool.InvokableTool).InvokableRun(ctx, `{"request":"research this"}`)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.ElementsMatch(t, []string{"read", "grep", "write", "edit", "bash", "load_skill"}, toolNames)
	require.Equal(t, "bot:tg:-100", runtimeNamespace, "subagent runtime calls must share the main turn namespace")
	require.Equal(t, "run_sub", runtimeRunID)
	joined := strings.Join(toolResults, "\n")
	require.Contains(t, joined, `<loaded_skill name="searxng"`)
	require.Contains(t, joined, "SEARXNG_SKILL_BODY")
	require.Contains(t, joined, "[Skill Error] requested skill is not available.")
	require.NotContains(t, joined, "RICH_SKILL_BODY")
	require.Contains(t, joined, "NOTES_CONTENT /workspace/notes.txt")
	require.True(t, tc.hasLoadedSkill("searxng"))
	require.False(t, tc.hasLoadedSkill("rich-message"))

	require.Less(t, len(out), 400)
	require.Contains(t, out, "[subagent result truncated: ")
	require.True(t, strings.HasPrefix(out, strings.Repeat("R", 150)))
	require.True(t, strings.HasSuffix(out, strings.Repeat("R", 150)))
}

func TestBuildSubAgentToolRejectsNonCanonicalSkill(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(model.Close)
	_, err := buildSubAgentTool(t.Context(), &config.SubAgentConfig{
		Name:   "bad",
		Model:  &config.Model{Name: "fixture", Model: "fixture", BaseUrl: model.URL, ApiKey: "fixture"},
		Skills: []string{"../disk"},
	}, nil)
	require.ErrorIs(t, err, errAgentV3InvalidSkillName)
}
