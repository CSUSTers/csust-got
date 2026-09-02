package agentv3

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"csust-got/config"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const malformedSearXNGToolArgs = `{"query":"https://private.example/workspace/private?token=secret-token"`

func TestSearXNGNativeToolSchemasAreExactAndDefaultOff(t *testing.T) {
	disabled := buildAgentV3Tools(nonRichAgentV3ChatConfig(), &config.AgentV3Config{}, agentV3SkillCatalog{}, nil)
	assert.Equal(t, []string{"read", "grep", "write", "edit", "bash"}, searXNGToolNames(t, disabled))

	client := newSearXNGNativeToolTestClient(t, testRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, assert.AnError
	}))
	enabled := buildAgentV3Tools(nonRichAgentV3ChatConfig(), &config.AgentV3Config{}, agentV3SkillCatalog{}, client)
	assert.Equal(t, []string{
		"searxng_web_search", "searxng_search_suggestions", "searxng_instance_info", "read", "grep", "write", "edit", "bash",
	}, searXNGToolNames(t, enabled))

	webSchema := searXNGToolSchema(t, enabled[0])
	assert.Equal(t, false, webSchema["additionalProperties"])
	assert.Equal(t, []string{"categories", "engines", "language", "min_score", "num_results", "pageno", "query", "response_format", "result_detail", "safesearch", "time_range"}, searXNGSchemaKeys(t, webSchema))
	assert.Equal(t, []any{"query"}, webSchema["required"])
	webProperties := webSchema["properties"].(map[string]any)
	assert.Equal(t, []any{"day", "week", "month", "year"}, webProperties["time_range"].(map[string]any)["enum"])
	assert.Equal(t, []any{float64(0), float64(1), float64(2)}, webProperties["safesearch"].(map[string]any)["enum"])
	assert.Equal(t, float64(1), webProperties["num_results"].(map[string]any)["minimum"])
	assert.Equal(t, float64(3), webProperties["num_results"].(map[string]any)["maximum"])
	minScore := webProperties["min_score"].(map[string]any)
	assert.Equal(t, "Optional finite minimum result score.", minScore["description"])
	assert.NotContains(t, minScore, "minimum")
	assert.NotContains(t, minScore, "maximum")

	suggestionSchema := searXNGToolSchema(t, enabled[1])
	assert.Equal(t, []string{"language", "query"}, searXNGSchemaKeys(t, suggestionSchema))
	assert.Equal(t, []any{"query"}, suggestionSchema["required"])
	instanceSchema := searXNGToolSchema(t, enabled[2])
	assert.Equal(t, []string{"category", "include_disabled", "include_engines"}, searXNGSchemaKeys(t, instanceSchema))
	assert.NotContains(t, searXNGToolSchemaJSON(t, enabled[0]), "base_url")
	for _, forbidden := range []string{"\"url\"", "host", "scheme", "port"} {
		assert.NotContains(t, searXNGToolSchemaJSON(t, enabled[0]), forbidden)
	}

	withoutSearXNG := agentV3ToolDefinitionsText(false, false, false)
	withSearXNG := agentV3ToolDefinitionsText(false, false, true)
	for _, name := range []string{"searxng_web_search", "searxng_search_suggestions", "searxng_instance_info"} {
		assert.NotContains(t, withoutSearXNG, name)
		assert.Contains(t, withSearXNG, name)
	}
	assert.NotEqual(t, hashString(withoutSearXNG), hashString(withSearXNG))
}

func TestSearXNGBuiltinRequiresEnableAndBuiltinInjection(t *testing.T) {
	cfg := &config.AgentV3Config{Skills: config.AgentV3SkillsConfig{SearXNG: testSearXNGConfig("https://search.example.org")}}
	snapshot := buildAgentV3BuiltinSkillSnapshot(nonRichAgentV3ChatConfig(), cfg)
	require.Len(t, snapshot.Skills, 1)
	skill := snapshot.Skills[0]
	assert.Equal(t, "searxng", skill.Name)
	assert.Equal(t, agentV3SkillSourceBuiltin, skill.Source)
	assert.Equal(t, agentV3SkillContentSHA256(skill.Content), skill.SHA256)
	for _, want := range []string{
		"searxng_web_search", "query", "pageno", "time_range", "language", "safesearch", "min_score", "num_results", "categories", "engines", "response_format", "result_detail",
		"searxng_search_suggestions", "searxng_instance_info", "include_engines", "include_disabled", "category", "fixed configured origin", "load_skill(name=\"searxng\")", "untrusted",
	} {
		assert.Contains(t, skill.Content, want)
	}

	disabled := false
	noInjection := *cfg
	noInjection.Skills.InjectBuiltin = &disabled
	assert.Empty(t, buildAgentV3BuiltinSkillSnapshot(nonRichAgentV3ChatConfig(), &noInjection).Skills)
	noEnable := *cfg
	noEnable.Skills.SearXNG.Enable = false
	assert.Empty(t, buildAgentV3BuiltinSkillSnapshot(nonRichAgentV3ChatConfig(), &noEnable).Skills)

	startup, err := loadAgentV3StartupSkillSnapshots(t.Context(), cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, startup.SearXNG)
	startup, err = loadAgentV3StartupSkillSnapshots(t.Context(), &noInjection, nil)
	require.NoError(t, err)
	assert.Nil(t, startup.SearXNG)
}

func TestSearXNGToolsRequireSuccessfulCurrentTurnLoadWithZeroIO(t *testing.T) {
	var getenvCalls atomic.Int32
	var transportCalls atomic.Int32
	cfg := testSearXNGConfig("https://search.example.org")
	cfg.UsernameEnv, cfg.PasswordEnv = "SEARXNG_TEST_USER", "SEARXNG_TEST_PASSWORD"
	client := newTestSearXNGClient(t, cfg)
	client.getenv = func(string) string {
		getenvCalls.Add(1)
		return "credential"
	}
	client.httpClient = &http.Client{Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		return searXNGNativeHTTPResponse(`{"results":[]}`), nil
	})}
	tools := buildAgentV3Tools(nonRichAgentV3ChatConfig(), &config.AgentV3Config{}, agentV3SkillCatalog{}, client)
	tc := &TurnContext{V3: &AgentV3TurnState{SkillCatalog: searXNGNativeToolCatalog(t, false), loadedSkillNames: map[string]struct{}{}}}
	ctx := WithTurnContext(t.Context(), tc)

	for index, args := range []string{`{"query":"tea"}`, `{"query":"tea"}`, `{}`} {
		out, err := tools[index].(tool.InvokableTool).InvokableRun(ctx, args)
		require.NoError(t, err)
		assert.Equal(t, agentV3SearXNGActivationRequired, out)
	}
	out, err := tools[0].(tool.InvokableTool).InvokableRun(ctx, malformedSearXNGToolArgs)
	require.NoError(t, err)
	assert.Equal(t, agentV3SearXNGActivationRequired, out)
	assert.Zero(t, getenvCalls.Load())
	assert.Zero(t, transportCalls.Load())
	assert.False(t, tc.richMessageSkillLoadedForFinal())

	_, err = tools[0].(tool.InvokableTool).InvokableRun(t.Context(), `{"query":"tea"}`)
	require.ErrorIs(t, err, errNoTurnContext)
	assert.Zero(t, getenvCalls.Load())
	assert.Zero(t, transportCalls.Load())
}

func TestSearXNGToolsWorkAfterLoadAndDoNotAuthorizeRichOutput(t *testing.T) {
	oldConfig := config.BotConfig
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}
	t.Cleanup(func() { config.BotConfig = oldConfig })

	var calls atomic.Int32
	client := newSearXNGNativeToolTestClient(t, testRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		switch req.URL.Path {
		case "/search":
			return searXNGNativeHTTPResponse(`{"results":[{"title":"Result","url":"https://example.org/result","content":"Summary"}]}`), nil
		case "/autocompleter":
			return searXNGNativeHTTPResponse(`["beta","alpha"]`), nil
		case "/config":
			return searXNGNativeHTTPResponse(`{"instance_name":"Fixture","categories":["general"]}`), nil
		default:
			return nil, assert.AnError
		}
	}))
	tools := buildAgentV3Tools(richAgentV3ChatConfig(), &config.AgentV3Config{}, agentV3SkillCatalog{}, client)
	tc := &TurnContext{Config: richAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: searXNGNativeToolCatalog(t, true), loadedSkillNames: map[string]struct{}{}}}
	ctx := WithTurnContext(t.Context(), tc)

	loaded, err := (&loadSkillTool{}).InvokableRun(ctx, `{"name":"searxng"}`)
	require.NoError(t, err)
	assert.Contains(t, loaded, `<loaded_skill name="searxng"`)
	assert.False(t, tc.richMessageSkillLoadedForFinal())

	web, err := tools[0].(tool.InvokableTool).InvokableRun(ctx, `{"query":"tea"}`)
	require.NoError(t, err)
	assert.Contains(t, web, "title: Result")
	suggestions, err := tools[1].(tool.InvokableTool).InvokableRun(ctx, `{"query":"tea"}`)
	require.NoError(t, err)
	assert.Equal(t, `["alpha","beta"]`, suggestions)
	instance, err := tools[2].(tool.InvokableTool).InvokableRun(ctx, `{}`)
	require.NoError(t, err)
	assert.Contains(t, instance, `"instance_name":"Fixture"`)
	assert.Equal(t, int32(3), calls.Load())

	for _, testCase := range []struct {
		name      string
		tool      tool.InvokableTool
		nilClient tool.InvokableTool
	}{
		{agentV3ToolSearXNGWebSearch, tools[0].(tool.InvokableTool), &searXNGWebSearchTool{}},
		{agentV3ToolSearXNGSuggestions, tools[1].(tool.InvokableTool), &searXNGSuggestionsTool{}},
		{agentV3ToolSearXNGInstanceInfo, tools[2].(tool.InvokableTool), &searXNGInstanceInfoTool{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			out, err := testCase.tool.InvokableRun(ctx, malformedSearXNGToolArgs)
			assert.Empty(t, out)
			requireSearXNGNativeToolFailure(t, err, testCase.name, searXNGErrorRequestFailed)

			out, err = testCase.nilClient.InvokableRun(ctx, `{}`)
			assert.Empty(t, out)
			requireSearXNGNativeToolFailure(t, err, testCase.name, searXNGErrorUnavailable)
		})
	}
	assert.Equal(t, int32(3), calls.Load())

	_, err = (&loadSkillTool{}).InvokableRun(ctx, `{"name":"rich-message"}`)
	require.NoError(t, err)
	assert.True(t, tc.richMessageSkillLoadedForFinal())
}

func TestSearXNGWebSearchToolAcceptsFiniteOutOfRangeMinScores(t *testing.T) {
	client := newSearXNGNativeToolTestClient(t, testRoundTripper(func(*http.Request) (*http.Response, error) {
		return searXNGNativeHTTPResponse(`{"results":[{"title":"Below negative threshold","url":"https://example.org/below","content":"below","score":-2},{"title":"Negative threshold match","url":"https://example.org/negative","content":"negative","score":-0.25},{"title":"High threshold match","url":"https://example.org/high","content":"high","score":2}]}`), nil
	}))
	webTool := &searXNGWebSearchTool{client: client}
	tc := &TurnContext{V3: &AgentV3TurnState{
		SkillCatalog:     searXNGNativeToolCatalog(t, false),
		loadedSkillNames: map[string]struct{}{agentV3SearXNGSkillName: {}},
	}}
	ctx := WithTurnContext(t.Context(), tc)

	for _, testCase := range []struct {
		name     string
		args     string
		contains []string
		absent   []string
	}{
		{
			name:     "negative threshold",
			args:     `{"query":"tea","min_score":-0.5}`,
			contains: []string{"Negative threshold match", "High threshold match"},
			absent:   []string{"Below negative threshold"},
		},
		{
			name:     "threshold above one",
			args:     `{"query":"tea","min_score":1.5}`,
			contains: []string{"High threshold match"},
			absent:   []string{"Below negative threshold", "Negative threshold match"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			output, err := webTool.InvokableRun(ctx, testCase.args)
			require.NoError(t, err)
			for _, want := range testCase.contains {
				assert.Contains(t, output, want)
			}
			for _, unwanted := range testCase.absent {
				assert.NotContains(t, output, unwanted)
			}
		})
	}
}

func TestSearXNGNativeToolsPrecedeAndShadowSameNameMCPOWithWarning(t *testing.T) {
	previous := zap.L()
	core, logs := observer.New(zap.WarnLevel)
	zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(func() { zap.ReplaceGlobals(previous) })

	client := newSearXNGNativeToolTestClient(t, testRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, assert.AnError
	}))
	configured := &searXNGNamedTestTool{name: agentV3ToolSearXNGWebSearch}
	warnAgentV3SearXNGToolCollisions(t.Context(), "search-chat", client, []tool.BaseTool{configured, configured})
	warnings := logs.FilterMessage("agentv3/agent: native SearXNG tool selected by native-first registration").All()
	require.Len(t, warnings, 1)
	assert.Equal(t, "search-chat", warnings[0].ContextMap()["chat"])
	assert.Equal(t, agentV3ToolSearXNGWebSearch, warnings[0].ContextMap()["tool"])

	native := buildAgentV3Tools(nonRichAgentV3ChatConfig(), &config.AgentV3Config{}, agentV3SkillCatalog{}, client)
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{
		Name:     "search-chat",
		Model:    &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("ok", nil)}}},
		Tools:    append(native, configured),
		MaxSteps: 4,
	})
	require.NoError(t, err)
	assert.IsType(t, &searXNGWebSearchTool{}, agent.invokables[agentV3ToolSearXNGWebSearch])
}

func TestSearXNGSkillAndToolStateAreIsolatedAcrossTurns(t *testing.T) {
	oldConfig := config.BotConfig
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}
	t.Cleanup(func() { config.BotConfig = oldConfig })

	var calls atomic.Int32
	client := newSearXNGNativeToolTestClient(t, testRoundTripper(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return searXNGNativeHTTPResponse(`{"results":[]}`), nil
	}))
	webTool := buildAgentV3Tools(richAgentV3ChatConfig(), &config.AgentV3Config{}, agentV3SkillCatalog{}, client)[0].(tool.InvokableTool)
	catalog := searXNGNativeToolCatalog(t, true)
	first := &TurnContext{Config: richAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: catalog, loadedSkillNames: map[string]struct{}{}}}
	second := &TurnContext{Config: richAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: catalog, loadedSkillNames: map[string]struct{}{}}}
	firstCtx, secondCtx := WithTurnContext(t.Context(), first), WithTurnContext(t.Context(), second)

	_, err := (&loadSkillTool{}).InvokableRun(firstCtx, `{"name":"searxng"}`)
	require.NoError(t, err)
	firstOut, err := webTool.InvokableRun(firstCtx, `{"query":"tea"}`)
	require.NoError(t, err)
	assert.NotEqual(t, agentV3SearXNGActivationRequired, firstOut)
	secondOut, err := webTool.InvokableRun(secondCtx, `{"query":"tea"}`)
	require.NoError(t, err)
	assert.Equal(t, agentV3SearXNGActivationRequired, secondOut)
	assert.Equal(t, int32(1), calls.Load())
	assert.False(t, first.richMessageSkillLoadedForFinal())
	assert.False(t, second.richMessageSkillLoadedForFinal())

	_, err = (&loadSkillTool{}).InvokableRun(firstCtx, `{"name":"rich-message"}`)
	require.NoError(t, err)
	assert.True(t, first.richMessageSkillLoadedForFinal())
	assert.False(t, second.richMessageSkillLoadedForFinal())
}

func newSearXNGNativeToolTestClient(t *testing.T, transport http.RoundTripper) *searXNGClient {
	t.Helper()
	client := newTestSearXNGClient(t, testSearXNGConfig("https://search.example.org"))
	client.httpClient = &http.Client{Transport: transport}
	return client
}

func searXNGNativeToolCatalog(t *testing.T, includeRich bool) agentV3SkillCatalog {
	t.Helper()
	descriptors := []agentV3SkillDescriptor{{
		Name:        "searxng",
		Description: "Search through the configured SearXNG tools.",
		Content:     "# SearXNG\nSearch through the configured SearXNG tools.\n",
	}}
	if includeRich {
		descriptors = append(descriptors, agentV3SkillDescriptor{
			Name:        "rich-message",
			Description: "Render rich output.",
			Content:     agentV3RichMessageSkillContract(true),
		})
	}
	snapshot, err := newAgentV3SkillSnapshot(agentV3SkillSourceBuiltin, descriptors)
	require.NoError(t, err)
	catalog, _, err := mergeAgentV3SkillSnapshots(snapshot)
	require.NoError(t, err)
	return catalog
}

func searXNGToolNames(t *testing.T, tools []tool.BaseTool) []string {
	t.Helper()
	names := make([]string, 0, len(tools))
	for _, item := range tools {
		info, err := item.Info(t.Context())
		require.NoError(t, err)
		names = append(names, info.Name)
	}
	return names
}

func searXNGToolSchema(t *testing.T, item tool.BaseTool) map[string]any {
	t.Helper()
	info, err := item.Info(t.Context())
	require.NoError(t, err)
	params, err := info.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	encoded, err := json.Marshal(params)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	return decoded
}

func searXNGToolSchemaJSON(t *testing.T, item tool.BaseTool) string {
	t.Helper()
	encoded, err := json.Marshal(searXNGToolSchema(t, item))
	require.NoError(t, err)
	return string(encoded)
}

func searXNGSchemaKeys(t *testing.T, value map[string]any) []string {
	t.Helper()
	properties, ok := value["properties"].(map[string]any)
	require.True(t, ok)
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func searXNGNativeHTTPResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func requireSearXNGNativeToolFailure(t *testing.T, err error, name, category string) {
	t.Helper()
	require.EqualError(t, err, name+": [SearXNG Error] "+category)
	for _, forbidden := range []string{"invalid character", "unexpected end", "searXNGWebSearchArgs", "searXNGSuggestionsArgs", "searXNGInstanceInfoArgs", "https://", "secret-token", "/workspace/private", "client is unavailable"} {
		assert.NotContains(t, err.Error(), forbidden)
	}
}

type searXNGNamedTestTool struct{ name string }

func (t *searXNGNamedTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "configured duplicate"}, nil
}

func (t *searXNGNamedTestTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "configured", nil
}
