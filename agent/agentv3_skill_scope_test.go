package agentv3

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/config"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestAgentV3SessionRestoresOrdinarySkillWithoutLoad(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "restored-skills", ContextMode: "reply_chain"}
	snapshot := testSkillSnapshot(agentV3SkillSourceBotLocal, []agentV3SkillDescriptor{
		{Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent, VirtualPath: "/skills/alpha/SKILL.md", environment: map[string]string{"TOKEN": "old"}},
		{Name: "searxng", Description: "Search the web.", Content: "# Search\nSearch the web.\n", VirtualPath: "/skills/searxng/SKILL.md"},
	})
	var searches atomic.Int32
	client := newSearXNGNativeToolTestClient(t, testRoundTripper(func(*http.Request) (*http.Response, error) {
		searches.Add(1)
		return searXNGNativeHTTPResponse(`["restored-result"]`), nil
	}))
	var requests []runtimeBashRequest
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/status" {
			_, _ = w.Write([]byte(`{"ok":true,"runtime_env":true,"bash_env_version":1}`))
			return
		}
		var req runtimeBashRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		requests = append(requests, req)
		_ = json.NewEncoder(w).Encode(runtimeBashResponse{BashEnvVersion: 1, Stdout: "restored-env"})
	}))
	t.Cleanup(runtime.Close)
	config.BotConfig.AgentV3.Runtime.Endpoint = runtime.URL
	loads := skillVisibilityCall("alpha-load", "alpha")
	loads.ToolCalls = append(loads.ToolCalls, skillVisibilityCall("search-load", "searxng").ToolCalls...)
	mdl := &scriptedToolModel{turns: [][]*schema.Message{
		{loads}, {schema.AssistantMessage("first final", nil)},
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{ID: "restored-bash", Function: schema.FunctionCall{Name: "bash", Arguments: `{"command":"printenv TOKEN"}`}},
			{ID: "restored-search", Function: schema.FunctionCall{Name: agentV3ToolSearXNGSuggestions, Arguments: `{"query":"query"}`}},
		}}}, {schema.AssistantMessage("second final", nil)},
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{ID: "rebuilt-bash", Function: schema.FunctionCall{Name: "bash", Arguments: `{"command":"printenv TOKEN"}`}},
			{ID: "rebuilt-search", Function: schema.FunctionCall{Name: agentV3ToolSearXNGSuggestions, Arguments: `{"query":"query"}`}},
		}}}, {schema.AssistantMessage("rebuilt final", nil)},
	}}
	compiled := f.compile(t, cfg, mdl, &loadSkillTool{}, &remoteBashTool{}, &searXNGSuggestionsTool{client: client})
	compiled.Agent.skillAdmission = &agentV3SkillAdmission{}
	compiled.AgentV3SkillSources = []agentV3SkillSnapshot{snapshot}
	firstID := f.chat(t, cfg, sessionMessage(100, 7, 0, "load ordinary skills"), nil)
	snapshot.Skills[0].environment = map[string]string{"TOKEN": "current"}
	compiled.AgentV3SkillSources = []agentV3SkillSnapshot{snapshot}
	current := sessionMessage(200, 7, 60, "use the same skills without loading again")
	current.ReplyTo = sessionMessage(firstID, 99, 0, "first final")
	secondID := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	require.Equal(t, int32(1), searches.Load(), "the continued session must pass the actual native skill gate without another load")
	require.Len(t, requests, 1)
	require.Equal(t, []runtimeSkillEnvLayer{{Source: agentV3SkillSourceBotLocal, Name: "alpha", Env: map[string]string{"TOKEN": "current"}}}, requests[0].SkillEnv)
	input := mdl.capturedInputs()[3]
	require.Contains(t, skillVisibilityResult(t, input, "restored-search"), "restored-result")
	require.Contains(t, skillVisibilityResult(t, input, "restored-bash"), "restored-env")
	config.BotConfig.AgentV3.Session.ContextOverflow.MaxTokens = 1
	current = sessionMessage(300, 7, 120, "continue after overflow rebuild")
	current.ReplyTo = sessionMessage(secondID, 99, 60, "second final")
	rebuiltID := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	require.Nil(t, f.node(t, rebuiltID).Parent, "overflow must actually rebuild a fresh root")
	require.Equal(t, int32(1), searches.Load(), "a rebuild losing the body cannot retain the native skill permission")
	require.Len(t, requests, 2)
	require.Empty(t, requests[1].SkillEnv, "a rebuild losing the body cannot retain skill environment")
	require.Equal(t, agentV3SearXNGActivationRequired, skillVisibilityResult(t, mdl.capturedInputs()[5], "rebuilt-search"))
}

func TestAgentV3SkillRestorationUsesCurrentAdmissionWithoutPreviewActivation(t *testing.T) {
	catalog := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{
		Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent, VirtualPath: "/skills/alpha/SKILL.md", environment: map[string]string{"KEY": "alpha"},
	})
	full := agentV3LoadedSkillResponse(catalog.ByName["alpha"])
	for _, scenario := range []struct {
		name      string
		admission *agentV3SkillAdmission
		catalog   agentV3SkillCatalog
		want      bool
	}{
		{"no loader", nil, catalog, false},
		{"restricted empty", &agentV3SkillAdmission{allowed: map[string]struct{}{}}, catalog, false},
		{"restricted other", &agentV3SkillAdmission{allowed: map[string]struct{}{"beta": {}}}, catalog, false},
		{"catalog removed", &agentV3SkillAdmission{}, agentV3SkillCatalog{}, false},
		{"unrestricted", &agentV3SkillAdmission{}, catalog, true},
		{"restricted allowed", &agentV3SkillAdmission{allowed: map[string]struct{}{"alpha": {}}}, catalog, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}}
			mdl.before = func(ctx context.Context, _ []*schema.Message) error {
				require.Equal(t, scenario.want, agentV3SkillLoaded(ctx, "alpha"))
				return nil
			}
			var tools []tool.BaseTool
			if scenario.admission != nil {
				tools = []tool.BaseTool{&loadSkillTool{allowed: scenario.admission.allowed}}
			}
			a, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Model: mdl, Tools: tools, MaxSteps: 2, SkillAdmission: scenario.admission})
			require.NoError(t, err)
			tc := &TurnContext{Config: nonRichAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: scenario.catalog}}
			ctx := WithTurnContext(t.Context(), tc)
			input := []*schema.Message{skillVisibilityCall("old", "alpha"), schema.ToolMessage(full, "old", schema.WithToolName(agentV3ToolLoadSkill)), schema.UserMessage("continue")}
			a.previewSessionModelInput(ctx, input)
			require.False(t, tc.hasLoadedSkill("alpha"), "token preview must not grant permissions")
			_, err = a.Generate(ctx, input)
			require.NoError(t, err)
			require.Equal(t, scenario.want, tc.hasLoadedSkill("alpha"))
			_, env := tc.runtimeEnvironment()
			require.Equal(t, scenario.want, len(env) == 1)
		})
	}
}

func TestAgentV3SkillRestorationPreservesFirstValidLoadOrder(t *testing.T) {
	local := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent, VirtualPath: "/skills/alpha/SKILL.md", environment: map[string]string{"KEY": "alpha"}})
	beta := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{Name: "beta", Description: "Beta skill.", Content: "# Beta\nBeta skill.\n", VirtualPath: "/skills/beta/SKILL.md", environment: map[string]string{"KEY": "beta"}})
	global := mustAgentV3SkillCatalog(t, agentV3SkillSourceRuntimeGlobal, agentV3SkillDescriptor{Name: "gamma", Description: "Gamma skill.", Content: "# Gamma\nGamma skill.\n", VirtualPath: "/skills/gamma/SKILL.md"})
	catalog, _, err := mergeAgentV3SkillSnapshots(mustSnapshot(t, local.ByName["alpha"]), mustSnapshot(t, beta.ByName["beta"]), mustSnapshot(t, global.ByName["gamma"]))
	require.NoError(t, err)
	input := make([]*schema.Message, 0, 11)
	for i, name := range []string{"alpha", "beta", "alpha", "beta", "gamma"} {
		id := strconv.Itoa(i)
		skill := catalog.ByName[name]
		if i == 0 {
			skill.Content += "obsolete instruction"
		}
		input = append(input, skillVisibilityCall(id, name), schema.ToolMessage(agentV3LoadedSkillResponse(skill), id, schema.WithToolName(agentV3ToolLoadSkill)))
	}
	input = append(input, schema.UserMessage("continue"))
	mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{sessionToolMessage("advance")}, {schema.AssistantMessage("done", nil)}}}}
	want := []runtimeSkillEnvLayer{
		{Source: agentV3SkillSourceBotLocal, Name: "beta", Env: map[string]string{"KEY": "beta"}},
		{Source: agentV3SkillSourceBotLocal, Name: "alpha", Env: map[string]string{"KEY": "alpha"}},
		{Source: agentV3SkillSourceRuntimeGlobal, Name: "gamma", SkillSHA256: global.ByName["gamma"].SHA256},
	}
	mdl.before = func(ctx context.Context, _ []*schema.Message) error {
		_, layers := agentV3RuntimeEnvironment(ctx)
		require.Equal(t, want, layers, "restoration order must be stable and subsequent steps must not append duplicate layers")
		return nil
	}
	a, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Model: mdl, Tools: []tool.BaseTool{&loadSkillTool{}, lookupTool{}}, MaxSteps: 3, SkillAdmission: &agentV3SkillAdmission{}})
	require.NoError(t, err)
	_, err = a.Generate(WithTurnContext(t.Context(), &TurnContext{Config: nonRichAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: catalog}}), input)
	require.NoError(t, err)
}

func TestAgentV3SessionRichRequiresCurrentParentExplicitLoad(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "explicit-rich", ContextMode: "reply_chain", Agent: &config.AgentOptions{Enable: true, Rich: true}}
	snapshot := buildAgentV3BuiltinSkillSnapshot(cfg, config.BotConfig.AgentV3)
	rich := mustTelegramRichEnvelope("**Rich result**")
	mdl := &scriptedToolModel{turns: [][]*schema.Message{
		{skillVisibilityCall("first-rich", "rich-message")}, {schema.AssistantMessage(rich, nil)},
		{schema.AssistantMessage(rich, nil)},
		{skillVisibilityCall("third-rich", "rich-message")}, {schema.AssistantMessage(rich, nil)},
	}}
	compiled := f.compile(t, cfg, mdl, &loadSkillTool{})
	compiled.Agent.skillAdmission = &agentV3SkillAdmission{}
	compiled.AgentV3SkillSources = []agentV3SkillSnapshot{snapshot}
	var mu sync.Mutex
	var deliveries []string
	transport := f.telegramClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	f.telegramClient.Transport = testRoundTripper(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/sendRichMessage") || strings.HasSuffix(req.URL.Path, "/sendMessage") {
			mu.Lock()
			deliveries = append(deliveries, req.URL.Path)
			mu.Unlock()
		}
		return transport.RoundTrip(req)
	})
	first := f.chat(t, cfg, sessionMessage(100, 7, 0, "first rich"), nil)
	current := sessionMessage(200, 7, 60, "no explicit rich load")
	current.ReplyTo = sessionMessage(first, 99, 0, "first final")
	second := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	current = sessionMessage(300, 7, 120, "explicit rich load again")
	current.ReplyTo = sessionMessage(second, 99, 60, "second final")
	f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, 3)
	require.True(t, strings.HasSuffix(deliveries[0], "/sendRichMessage"))
	require.True(t, strings.HasSuffix(deliveries[1], "/sendMessage"), "historical rich activation must produce a text fallback")
	require.True(t, strings.HasSuffix(deliveries[2], "/sendRichMessage"))
	full := skillVisibilityResult(t, mdl.capturedInputs()[1], "first-rich")
	reused := skillVisibilityResult(t, mdl.capturedInputs()[4], "third-rich")
	require.Less(t, len(reused), len(full), "explicit rich activation still reuses a visible full body")
}

func TestADKSubAgentSkillScopeKeepsEnvironmentAndRichPermissionPrivate(t *testing.T) {
	old := config.BotConfig
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}
	t.Cleanup(func() { config.BotConfig = old })
	catalog := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{
		Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent, VirtualPath: "/skills/alpha/SKILL.md", environment: map[string]string{"KEY": "child"},
	})
	richSnapshot := buildAgentV3BuiltinSkillSnapshot(richAgentV3ChatConfig(), config.BotConfig.AgentV3)
	parent := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{
		Name: "parent", Description: "Parent skill.", Content: "# Parent\nParent skill.\n", VirtualPath: "/skills/parent/SKILL.md", environment: map[string]string{"KEY": "parent"},
	})
	catalog, _, err := mergeAgentV3SkillSnapshots(mustSnapshot(t, catalog.ByName["alpha"]), mustSnapshot(t, parent.ByName["parent"]), richSnapshot)
	require.NoError(t, err)
	var mu sync.Mutex
	var requests []runtimeBashRequest
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/status" {
			_, _ = w.Write([]byte(`{"ok":true,"runtime_env":true,"bash_env_version":1}`))
			return
		}
		var req runtimeBashRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(runtimeBashResponse{BashEnvVersion: 1, Stdout: "ok"})
	}))
	t.Cleanup(runtime.Close)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body fakeOpenAIRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		names := make([]string, 0, len(body.Tools))
		for _, item := range body.Tools {
			names = append(names, item.Function.Name)
		}
		require.Contains(t, names, "get_message")
		require.NotContains(t, names, "get_image", "parent direct tools must stay private")
		require.NotContains(t, names, "update_progress", "parent skill bundle tools must stay private")
		results := 0
		for _, message := range body.Messages {
			if message.Role == "tool" {
				results++
			}
		}
		var calls []schema.ToolCall
		switch results {
		case 0:
			calls = []schema.ToolCall{{ID: "before-load", Type: "function", Function: schema.FunctionCall{Name: "bash", Arguments: `{"command":"printenv KEY"}`}}}
		case 1:
			calls = skillVisibilityCall("child-alpha", "alpha").ToolCalls
			calls = append(calls, skillVisibilityCall("child-rich", "rich-message").ToolCalls...)
		case 3:
			calls = []schema.ToolCall{{ID: "after-load", Type: "function", Function: schema.FunctionCall{Name: "bash", Arguments: `{"command":"printenv KEY"}`}}}
		default:
			writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "content": "child complete"}, "stop")
			return
		}
		wireCalls := make([]map[string]any, 0, len(calls))
		for i, call := range calls {
			wireCalls = append(wireCalls, map[string]any{"index": i, "id": call.ID, "type": "function", "function": map[string]any{"name": call.Function.Name, "arguments": call.Function.Arguments}})
		}
		writeFakeOpenAIResponse(w, body.Stream, map[string]any{"role": "assistant", "content": "", "tool_calls": wireCalls}, "tool_calls")
	}))
	t.Cleanup(model.Close)
	runtimeClient := &RemoteRuntimeClient{Endpoint: runtime.URL, HTTPClient: runtime.Client(), CommandTimeout: time.Second, MaxOutputChars: 1000}
	tc := &TurnContext{Config: richAgentV3ChatConfig(), Namespace: "bot:tg:-100", RunID: "parent-run", RuntimeClient: runtimeClient, V3: &AgentV3TurnState{SkillCatalog: catalog, runtimeEnv: map[string]string{"BASE": "shared"}}}
	ctx := WithTurnContext(t.Context(), tc)
	_, err = (&loadSkillTool{}).InvokableRun(ctx, `{"name":"parent"}`)
	require.NoError(t, err)
	tc.Config.Agent.Tools = []string{"get_image"}
	tc.Config.Agent.Skills = []*config.SkillConfig{{Tools: []string{"update_progress"}}}
	tc.Config.Agent.SubAgents = []*config.SubAgentConfig{{Name: "isolated", Description: "scope test", Model: &config.Model{Name: "fixture", Model: "fixture", BaseUrl: model.URL, ApiKey: "fixture"}, Runtime: true, Skills: []string{"alpha", "rich-message"}, SkillBundles: []*config.SkillConfig{{Tools: []string{"get_message"}, SystemPromptAddon: "Keep child work private."}}, MaxSteps: 8}}
	parentTools, err := buildConfiguredAgentTools(ctx, "parent", tc.Config.Agent, nil)
	require.NoError(t, err)
	require.Len(t, parentTools, 3)
	sub := parentTools[2]
	for range 2 {
		out, err := sub.(tool.InvokableTool).InvokableRun(ctx, `{"request":"inspect the environment"}`)
		require.NoError(t, err)
		require.Equal(t, "child complete", out)
		require.False(t, tc.hasLoadedSkill("alpha"))
		require.False(t, tc.richMessageSkillLoadedForFinal(), "successful child rich load cannot authorize the parent")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 4)
	for i, request := range requests {
		require.Equal(t, tc.Namespace, request.Namespace)
		require.Equal(t, tc.RunID, request.RunID)
		require.Equal(t, map[string]string{"BASE": "shared"}, request.Env)
		if i%2 == 0 {
			require.Empty(t, request.SkillEnv, "each real ADK invocation starts without parent or sibling skill environment")
		} else {
			require.Equal(t, []runtimeSkillEnvLayer{{Source: agentV3SkillSourceBotLocal, Name: "alpha", Env: map[string]string{"KEY": "child"}}}, request.SkillEnv)
		}
	}
	require.Same(t, runtimeClient, tc.RuntimeClient)
	_, layers := tc.runtimeEnvironment()
	require.Equal(t, []runtimeSkillEnvLayer{{Source: agentV3SkillSourceBotLocal, Name: "parent", Env: map[string]string{"KEY": "parent"}}}, layers)
	_, err = (&loadSkillTool{}).InvokableRun(ctx, `{"name":"rich-message"}`)
	require.NoError(t, err)
	require.True(t, tc.richMessageSkillLoadedForFinal(), "explicit parent load still updates the state read by final delivery")
}

func TestSearXNGToolsUseCallLocalSkillScope(t *testing.T) {
	var requests atomic.Int32
	client := newSearXNGNativeToolTestClient(t, testRoundTripper(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return searXNGNativeHTTPResponse(`["child-result"]`), nil
	}))
	tc := &TurnContext{V3: &AgentV3TurnState{SkillCatalog: searXNGNativeToolCatalog(t, false)}}
	parentCtx := WithTurnContext(t.Context(), tc)
	_, err := (&loadSkillTool{}).InvokableRun(parentCtx, `{"name":"searxng"}`)
	require.NoError(t, err)
	childCtx := beginAgentV3SkillScope(parentCtx, true)
	search := &searXNGSuggestionsTool{client: client}
	out, err := search.InvokableRun(childCtx, `{"query":"query"}`)
	require.NoError(t, err)
	require.Equal(t, agentV3SearXNGActivationRequired, out)
	require.Zero(t, requests.Load(), "a parent's active skill cannot pass a child's native tool gate")
	_, err = (&loadSkillTool{}).InvokableRun(childCtx, `{"name":"searxng"}`)
	require.NoError(t, err)
	out, err = search.InvokableRun(childCtx, `{"query":"query"}`)
	require.NoError(t, err)
	require.Contains(t, out, "child-result")
	require.Equal(t, int32(1), requests.Load())
	require.True(t, tc.hasLoadedSkill("searxng"))
}
