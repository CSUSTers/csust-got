package agentv3

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestSessionContextEstimateTextFormula(t *testing.T) {
	for _, test := range []struct {
		text   string
		tokens int64
	}{
		{"", 0}, {"a", 1}, {"abcd", 1}, {"abcde", 2}, {"abcd中ef", 4},
		{"汉かな한é😀", 12}, {"e\u0301", 3}, {"\t\n \x00", 1},
	} {
		t.Run(test.text, func(t *testing.T) {
			c := newSessionTokenCounter(math.MaxInt64)
			c.text(test.text)
			require.Equal(t, test.tokens, c.Tokens)
			require.False(t, c.Capped)
		})
	}
	c := newSessionTokenCounter(math.MaxInt64)
	c.text("a")
	c.text("b")
	require.EqualValues(t, 2, c.Tokens, "field boundaries end ASCII runs")
}

func TestSessionContextEstimateActualThresholdBoundary(t *testing.T) {
	a := &CustomAgent{maxSteps: 2}
	for _, want := range []int64{199999, 200000, 200001} {
		input := []*schema.Message{schema.UserMessage(strings.Repeat("a", int((want-48)*4)))}
		estimate, err := a.estimateSessionContext(t.Context(), input, 200000)
		require.NoError(t, err)
		require.Equal(t, want, estimate.Tokens)
		require.Equal(t, want > 200000, estimate.Capped)
		require.Equal(t, sessionContextEstimateMethod, estimate.Method)
	}
	input := []*schema.Message{schema.UserMessage(strings.Repeat("长", 100000))}
	estimate, err := a.estimateSessionContext(t.Context(), input, 200000)
	require.NoError(t, err)
	require.EqualValues(t, 200001, estimate.Tokens)
	require.True(t, estimate.Capped)
}

func TestSessionContextEstimateSaturation(t *testing.T) {
	c := newSessionTokenCounter(200000)
	c.add(math.MaxInt64)
	c.add(math.MaxInt64)
	require.EqualValues(t, 200001, c.Tokens)
	require.True(t, c.Capped)
	c = newSessionTokenCounter(math.MaxInt64)
	c.add(math.MaxInt64 - 1)
	c.add(1)
	require.EqualValues(t, math.MaxInt64, c.Tokens)
	require.False(t, c.Capped, "exact maximum is not overflow")
	c.add(1)
	require.EqualValues(t, math.MaxInt64, c.Tokens)
	require.True(t, c.Capped, "maximum threshold uses the overflow flag")
	c = newSessionTokenCounter(math.MaxInt64 - 1)
	c.add(math.MaxInt64)
	require.True(t, c.Capped)
	require.EqualValues(t, math.MaxInt64, c.Tokens)
	a := &CustomAgent{maxSteps: 2, sessionToolEstimate: sessionContextEstimate{Tokens: math.MaxInt64}}
	estimate, err := a.estimateSessionContext(t.Context(), []*schema.Message{schema.UserMessage("a")}, math.MaxInt64)
	require.NoError(t, err)
	require.EqualValues(t, math.MaxInt64, estimate.Tokens)
	require.True(t, estimate.Capped)
}

func TestSessionContextEstimateMediaTransportAndText(t *testing.T) {
	a := &CustomAgent{maxSteps: 2}
	for _, test := range []struct {
		kind   schema.ChatMessagePartType
		budget int64
	}{
		{schema.ChatMessagePartTypeImageURL, 4096}, {schema.ChatMessagePartTypeAudioURL, 8192},
		{schema.ChatMessagePartTypeVideoURL, 16384}, {schema.ChatMessagePartTypeFileURL, 8192},
	} {
		for _, legacy := range []bool{false, true} {
			for _, transport := range []string{"https://example.invalid/media", "data:application/octet-stream;base64," + strings.Repeat("A", 2<<20), strings.Repeat("A", 2<<20)} {
				message := sessionEstimateMedia(test.kind, transport, legacy)
				estimate, err := a.estimateSessionContext(t.Context(), []*schema.Message{message}, 200000)
				require.NoError(t, err)
				require.Equal(t, int64(48)+test.budget, estimate.Tokens)
				require.False(t, estimate.Capped)
				message.Content = "abcd中ef"
				estimate, err = a.estimateSessionContext(t.Context(), []*schema.Message{message}, 200000)
				require.NoError(t, err)
				require.Equal(t, int64(52)+test.budget, estimate.Tokens)
			}
		}
	}
	for _, text := range []string{strings.Repeat("A", 800000), "data:image/png;base64," + strings.Repeat("A", 800000), `{"image_url":"` + strings.Repeat("A", 800000) + `"}`} {
		estimate, err := a.estimateSessionContext(t.Context(), []*schema.Message{schema.UserMessage(text)}, 200000)
		require.NoError(t, err)
		require.True(t, estimate.Capped, "base64/JSON in a text position must remain text")
	}
	input := sessionEstimateMedia(schema.ChatMessagePartTypeImageURL, "short", false)
	input.UserInputMultiContent = append(input.UserInputMultiContent, input.UserInputMultiContent[0])
	estimate, err := a.estimateSessionContext(t.Context(), []*schema.Message{input}, 200000)
	require.NoError(t, err)
	require.EqualValues(t, 48+2*4096, estimate.Tokens)
}

func sessionEstimateMedia(kind schema.ChatMessagePartType, payload string, legacy bool) *schema.Message {
	m := schema.UserMessage("")
	if legacy {
		encoded, err := json.Marshal(map[string]any{"role": schema.User, "multi_content": []any{map[string]any{"type": kind, string(kind): map[string]any{"url": payload}}}})
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(encoded, m); err != nil {
			panic(err)
		}
		return m
	}
	common := schema.MessagePartCommon{Base64Data: &payload}
	if strings.Contains(payload, ":") {
		common.URL, common.Base64Data = &payload, nil
	}
	part := schema.MessageInputPart{Type: kind}
	switch kind {
	case schema.ChatMessagePartTypeImageURL:
		part.Image = &schema.MessageInputImage{MessagePartCommon: common}
	case schema.ChatMessagePartTypeAudioURL:
		part.Audio = &schema.MessageInputAudio{MessagePartCommon: common}
	case schema.ChatMessagePartTypeVideoURL:
		part.Video = &schema.MessageInputVideo{MessagePartCommon: common}
	case schema.ChatMessagePartTypeFileURL:
		part.File = &schema.MessageInputFile{MessagePartCommon: common}
	default:
		panic("fixture requires a media type")
	}
	m.UserInputMultiContent = []schema.MessageInputPart{part}
	return m
}

func TestSessionContextEstimateMetadataAndHistoricalFields(t *testing.T) {
	message := &schema.Message{Role: schema.Assistant, Content: "a", Name: "b", ReasoningContent: "中", ToolCalls: []schema.ToolCall{{ID: "id", Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{"image_url":"data:image;base64,AAAA"}`}, Extra: map[string]any{"n": json.Number("9007199254740993")}}}, Extra: map[string]any{"text": "abcd中ef", "bytes": []byte{1, 2, 3, 4}, "controls": "\n\t<>&", "nested": []any{true, nil, 1.25}}}
	c := newSessionTokenCounter(math.MaxInt64)
	require.NoError(t, c.message(message))
	want := newSessionTokenCounter(math.MaxInt64)
	want.add(16)
	for _, text := range []string{message.Content, message.Name, message.ReasoningContent, "id", "function", "lookup", message.ToolCalls[0].Function.Arguments} {
		want.text(text)
	}
	for _, extra := range []map[string]any{message.Extra, message.ToolCalls[0].Extra} {
		encoded, err := json.Marshal(extra)
		require.NoError(t, err)
		want.text(string(encoded))
	}
	require.Equal(t, want.Tokens, c.Tokens)
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: math.MaxInt}, FinishReason: strings.Repeat("x", 1000)}
	again := newSessionTokenCounter(math.MaxInt64)
	require.NoError(t, again.message(message))
	require.Equal(t, c.Tokens, again.Tokens, "usage/response metadata is not prompt text")
	message.AssistantGenMultiContent = []schema.MessageOutputPart{{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "中", Signature: "abcd"}}, {Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageOutputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: new(strings.Repeat("A", 1<<20))}}}}
	again = newSessionTokenCounter(math.MaxInt64)
	require.NoError(t, again.message(message))
	require.EqualValues(t, c.Tokens+3+4096, again.Tokens)
}

func TestSessionContextEstimateLargeVisibleFieldsAndSchemas(t *testing.T) {
	text := strings.Repeat("A", 800000)
	a := &CustomAgent{maxSteps: 2}
	for _, input := range [][]*schema.Message{
		{{Role: schema.User, ReasoningContent: text}},
		{{Role: schema.User, Extra: map[string]any{"image_url": text}}},
		{schema.AssistantMessage("", []schema.ToolCall{lookupToolCall("call", text)}), schema.ToolMessage("result", "call")},
		{schema.AssistantMessage("", []schema.ToolCall{lookupToolCall("call", "{}")}), schema.ToolMessage(text, "call")},
		{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: text}}}},
	} {
		estimate, err := a.estimateSessionContext(t.Context(), input, 200000)
		require.NoError(t, err)
		require.True(t, estimate.Capped)
		require.EqualValues(t, 200001, estimate.Tokens)
	}
	info := &schema.ToolInfo{Name: "small_name", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"q": {Type: schema.String, Desc: text}})}
	bound := newSessionCaptureAgent(t, &sessionEstimateBindingModel{}, []tool.BaseTool{&sessionEstimateInfoTool{info: info}}, 4)
	estimate, err := bound.estimateSessionContext(t.Context(), []*schema.Message{schema.UserMessage("short")}, 200000)
	require.NoError(t, err)
	require.True(t, estimate.Capped, "actual bound schema alone may exceed the threshold")
	message := &schema.Message{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeToolSearchResult, ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{info}}}}}
	estimate, err = a.estimateSessionContext(t.Context(), []*schema.Message{message}, 200000)
	require.NoError(t, err)
	require.True(t, estimate.Capped, "tool-search schema must count as visible schema text")
}

func TestSessionContextEstimateUnknownDiagnostics(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, message := range []*schema.Message{
		{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: "unknown"}}},
		{Role: schema.Assistant, AssistantGenMultiContent: []schema.MessageOutputPart{{Type: "unknown"}}},
		{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL}}},
		{Role: schema.User, Extra: cycle},
		{Role: schema.User, Extra: map[string]any{"callback": func() { t.Fatal("must never run") }}},
		{Role: schema.User, Extra: map[string]any{"custom": sessionEstimatePanicMarshaler{}}},
		{Role: schema.User, Extra: map[string]any{"number": math.NaN()}},
		{Role: schema.User, Extra: map[string]any{"raw": json.RawMessage(`{"image_url":"not media"}`)}},
	} {
		a := &CustomAgent{maxSteps: 2}
		_, err := a.estimateSessionContext(t.Context(), []*schema.Message{message}, 200000)
		require.ErrorIs(t, err, errSessionContextEstimate)
	}
	var legacy schema.Message
	require.NoError(t, json.Unmarshal([]byte(`{"role":"user","multi_content":[{"type":"unknown"}]}`), &legacy))
	_, err := (&CustomAgent{maxSteps: 2}).estimateSessionContext(t.Context(), []*schema.Message{&legacy}, 200000)
	require.ErrorIs(t, err, errSessionContextEstimate)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = (&CustomAgent{maxSteps: 2}).estimateSessionContext(ctx, nil, 1)
	require.ErrorIs(t, err, context.Canceled)
	_, err = (&CustomAgent{maxSteps: 2}).estimateSessionContext(t.Context(), nil, 0)
	require.ErrorIs(t, err, errSessionContextEstimate)
}

type sessionEstimatePanicMarshaler struct{}

func (sessionEstimatePanicMarshaler) MarshalJSON() ([]byte, error) {
	panic("must not run metadata code")
}

type sessionEstimateInfoTool struct {
	info  *schema.ToolInfo
	calls int
}

func (t *sessionEstimateInfoTool) Info(context.Context) (*schema.ToolInfo, error) {
	t.calls++
	return t.info, nil
}
func (*sessionEstimateInfoTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	panic("preview must not run tools")
}

type sessionEstimateBindingModel struct {
	sessionCaptureTestModel
	tools []*schema.ToolInfo
	binds int
}

func (m *sessionEstimateBindingModel) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.tools = infos
	m.binds++
	return m, nil
}

func TestSessionContextEstimatePreviewMatchesFirstActualModelInput(t *testing.T) {
	for _, steps := range []int{1, 4} {
		for _, v3 := range []bool{false, true} {
			var actual []*schema.Message
			modelCalls := 0
			mdl := &sessionEstimateBindingModel{sessionCaptureTestModel: sessionCaptureTestModel{stream: func(_ context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				modelCalls++
				actual = input
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("answer", nil)}), nil
			}}}
			info, _ := sessionCaptureToolDefinition(t, "params")
			testTool := &sessionEstimateInfoTool{info: info}
			a := newSessionCaptureAgent(t, mdl, []tool.BaseTool{testTool}, steps)
			input := []*schema.Message{schema.SystemMessage("prefix"), schema.ToolMessage("orphan", "missing"), schema.SystemMessage("memory"), schema.UserMessage("addition"), {Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("historic", "{}")}}, schema.ToolMessage("result", "historic"), sessionCaptureMultimodalInput(info)}
			ctx := t.Context()
			if v3 {
				ctx = WithTurnContext(ctx, &TurnContext{V3: &AgentV3TurnState{}})
			}
			capture := NewSessionCapture()
			ctx = WithSessionCapture(ctx, capture)
			preview := a.previewSessionModelInput(ctx, input)
			estimate, err := a.estimateSessionContext(ctx, input, 200000)
			require.NoError(t, err)
			require.False(t, capture.started)
			require.Zero(t, modelCalls)
			require.Equal(t, 1, testTool.calls)
			require.Equal(t, 1, mdl.binds)
			_, err = a.Generate(ctx, input)
			require.NoError(t, err)
			require.Equal(t, preview, actual)
			require.True(t, capture.Snapshot().Complete)
			require.Equal(t, input, capture.Snapshot().Input)
			require.Equal(t, a.sessionModelBaseline(ctx, input), capture.Snapshot().ModelInput)
			c := newSessionTokenCounter(math.MaxInt64)
			c.add(32)
			for _, tool := range mdl.tools {
				require.NoError(t, c.tool(tool))
			}
			for _, message := range actual {
				require.NoError(t, c.message(message))
			}
			require.Equal(t, c.Tokens, estimate.Tokens)
			if steps == 1 {
				require.Contains(t, actual[len(actual)-1].Content, finalTurnGuidance)
			} else {
				require.NotContains(t, actual[len(actual)-1].Content, finalTurnGuidance, "history tool calls do not consume this invocation's budget")
			}
		}
	}
}

func TestSessionContextEstimateToolsSchemasAndImmutableCache(t *testing.T) {
	params := map[string]*schema.ParameterInfo{"q": {Type: schema.String, Desc: strings.Repeat("parameter description 中", 2000), Required: true}}
	info := &schema.ToolInfo{Name: "search", Desc: "desc", ParamsOneOf: schema.NewParamsOneOfByParams(params)}
	definition, err := info.ToJSONSchema()
	require.NoError(t, err)
	equivalent := &schema.ToolInfo{Name: info.Name, Desc: info.Desc, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(definition)}
	first, err := estimateSessionTools(t.Context(), []*schema.ToolInfo{info})
	require.NoError(t, err)
	second, err := estimateSessionTools(t.Context(), []*schema.ToolInfo{equivalent})
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Greater(t, first.Tokens, int64(10000), "parameter descriptions, not just names, must count")
	a := newSessionCaptureAgent(t, &sessionEstimateBindingModel{}, []tool.BaseTool{&sessionEstimateInfoTool{info: info}}, 4)
	before, err := a.estimateSessionContext(t.Context(), []*schema.Message{schema.UserMessage("input")}, math.MaxInt64)
	require.NoError(t, err)
	info.Desc = "changed"
	params["q"].Desc = "changed"
	after, err := a.estimateSessionContext(t.Context(), []*schema.Message{schema.UserMessage("input")}, math.MaxInt64)
	require.NoError(t, err)
	require.Equal(t, before, after)
	for _, bad := range []*schema.ToolInfo{
		{Name: "nil", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"q": nil})},
		{Name: "custom", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{Default: sessionEstimatePanicMarshaler{}})},
		{Name: "custom extra", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{Extras: map[string]any{"x-provider": sessionEstimatePanicMarshaler{}}})},
	} {
		_, err = estimateSessionTools(t.Context(), []*schema.ToolInfo{bad})
		require.ErrorIs(t, err, errSessionContextEstimate)
	}
	parameter := &schema.ParameterInfo{Type: schema.Array}
	parameter.ElemInfo = parameter
	_, err = estimateSessionTools(t.Context(), []*schema.ToolInfo{{ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"p": parameter})}})
	require.ErrorIs(t, err, errSessionContextEstimate)
	s := &jsonschema.Schema{}
	s.AnyOf = []*jsonschema.Schema{s}
	_, err = estimateSessionTools(t.Context(), []*schema.ToolInfo{{ParamsOneOf: schema.NewParamsOneOfByJSONSchema(s)}})
	require.ErrorIs(t, err, errSessionContextEstimate)
}

func TestSessionContextEstimateBoundToolDiagnosticDoesNotBlockAgent(t *testing.T) {
	for _, test := range []struct {
		name string
		info *schema.ToolInfo
	}{
		{"dynamic metadata", &schema.ToolInfo{Name: "dynamic", Extra: map[string]any{"callback": func() { t.Fatal("estimation must not execute metadata") }}}},
		{"custom marshaler", &schema.ToolInfo{Name: "custom", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{Default: sessionEstimatePanicMarshaler{}})}},
		{"invalid JSON number", &schema.ToolInfo{Name: "invalid", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{Default: json.Number("not-a-number")})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			modelCalls := 0
			mdl := &sessionEstimateBindingModel{sessionCaptureTestModel: sessionCaptureTestModel{stream: func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				modelCalls++
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("normal answer", nil)}), nil
			}}}
			testTool := &sessionEstimateInfoTool{info: test.info}
			a, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "optional-session", Model: mdl, Tools: []tool.BaseTool{testTool}, MaxSteps: 4})
			require.NoError(t, err, "a successful WithTools must remain a successful agent construction")
			require.Equal(t, 1, mdl.binds)
			require.Same(t, test.info, mdl.tools[0], "estimate uses the infos that actually bound")
			require.ErrorIs(t, a.sessionToolEstimateErr, errSessionContextEstimate)
			input := []*schema.Message{schema.UserMessage("input")}
			for range 2 {
				estimate, err := a.estimateSessionContext(t.Context(), input, 200000)
				require.ErrorIs(t, err, errSessionContextEstimate)
				require.Equal(t, sessionContextEstimateMethod, estimate.Method)
			}
			require.Zero(t, modelCalls, "diagnostics must not execute the model")
			for _, captureEnabled := range []bool{false, true} {
				ctx := t.Context()
				capture := NewSessionCapture()
				if captureEnabled {
					ctx = WithSessionCapture(ctx, capture)
				}
				answer, err := a.Generate(ctx, input)
				require.NoError(t, err, "no-load/fallback invocation must ignore optional estimator failure")
				require.Equal(t, "normal answer", answer.Content)
				if captureEnabled {
					<-capture.Done()
					result := capture.Snapshot()
					require.NoError(t, result.Err)
					require.True(t, result.Complete)
				}
			}
			require.Equal(t, 2, modelCalls)
			require.Equal(t, 1, testTool.calls, "neither estimation nor invocation fetches Info again")
			require.Equal(t, 1, mdl.binds)
		})
	}
}

func BenchmarkSessionContextEstimate(b *testing.B) {
	a := &CustomAgent{maxSteps: 2}
	for name, message := range map[string]*schema.Message{
		"short": schema.UserMessage("short input"), "text1MiB": schema.UserMessage(strings.Repeat("A", 1<<20)),
		"imageURL":         sessionEstimateMedia(schema.ChatMessagePartTypeImageURL, "https://example.invalid/image", false),
		"imageDataURI1MiB": sessionEstimateMedia(schema.ChatMessagePartTypeImageURL, "data:image/png;base64,"+strings.Repeat("A", 1<<20), false),
		"imageBase64_8MiB": sessionEstimateMedia(schema.ChatMessagePartTypeImageURL, strings.Repeat("A", 8<<20), false),
	} {
		b.Run(name, func(b *testing.B) {
			input := []*schema.Message{message}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := a.estimateSessionContext(b.Context(), input, math.MaxInt64); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
