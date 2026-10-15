package agentv3

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/stretchr/testify/require"
)

type sessionCaptureRuntimeMetadata struct {
	Values []int `json:"-"`
}

func sessionCaptureToolDefinition(t *testing.T, representation string) (*schema.ToolInfo, func()) {
	t.Helper()
	info := &schema.ToolInfo{Name: "search", Desc: "tool definition", Extra: map[string]any{"provider": "test"}}
	if representation == "params" {
		params := map[string]*schema.ParameterInfo{
			"query": {Type: schema.Object, Required: true, Desc: "query object", SubParams: map[string]*schema.ParameterInfo{
				"tags": {Type: schema.Array, Required: true, ElemInfo: &schema.ParameterInfo{Type: schema.String, Enum: []string{"campus", "course"}}},
			}},
		}
		info.ParamsOneOf = schema.NewParamsOneOfByParams(params)
		return info, func() {
			params["query"].Desc = "changed"
			params["query"].SubParams["tags"].ElemInfo.Enum[0] = "changed"
			delete(params["query"].SubParams, "tags")
			params["added"] = &schema.ParameterInfo{Type: schema.Boolean}
		}
	}
	var definition jsonschema.Schema
	require.NoError(t, json.Unmarshal([]byte(`{"type":"object","properties":{"query":{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string","enum":["campus","course"]}}},"required":["tags"]}},"required":["query"],"$defs":{"term":{"anyOf":[{"type":"string"},{"type":"null"}]}},"additionalProperties":false,"default":{"provider":["test"]}}`), &definition))
	info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&definition)
	return info, func() {
		query, ok := definition.Properties.Get("query")
		require.True(t, ok)
		tags, ok := query.Properties.Get("tags")
		require.True(t, ok)
		tags.Items.Enum[0] = "changed"
		query.Properties.Set("added", &jsonschema.Schema{Type: "boolean"})
		definition.Required[0] = "changed"
		definition.Definitions["term"].AnyOf[0].Type = "boolean"
		definition.Default.(map[string]any)["provider"].([]any)[0] = "changed"
	}
}

func sessionCaptureJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

func requireSessionCaptureParamsRepresentation(t *testing.T, params *schema.ParamsOneOf, representation string) {
	t.Helper()
	require.NotNil(t, params)
	value := reflect.ValueOf(params).Elem()
	require.Equal(t, representation != "params", value.FieldByName("params").IsNil(), "params backing must match the constructor")
	require.Equal(t, representation != "jsonschema", value.FieldByName("jsonschema").IsNil(), "JSONSchema backing must match the constructor")
}

func requireSessionCaptureParamsDetached(t *testing.T, a, b *schema.ParamsOneOf) {
	t.Helper()
	require.NotSame(t, a, b)
	for _, field := range []string{"params", "jsonschema"} {
		require.Equal(t, reflect.ValueOf(a).Elem().FieldByName(field).IsNil(), reflect.ValueOf(b).Elem().FieldByName(field).IsNil(), "deepcopy must not normalize %s backing", field)
	}
	left := reflect.ValueOf(a).Elem().FieldByName("params")
	right := reflect.ValueOf(b).Elem().FieldByName("params")
	if !left.IsNil() {
		require.NotEqual(t, left.UnsafePointer(), right.UnsafePointer())
		query := left.MapIndex(reflect.ValueOf("query"))
		otherQuery := right.MapIndex(reflect.ValueOf("query"))
		require.NotEqual(t, query.UnsafePointer(), otherQuery.UnsafePointer())
		children := query.Elem().FieldByName("SubParams")
		otherChildren := otherQuery.Elem().FieldByName("SubParams")
		require.NotEqual(t, children.UnsafePointer(), otherChildren.UnsafePointer())
		tags := children.MapIndex(reflect.ValueOf("tags"))
		otherTags := otherChildren.MapIndex(reflect.ValueOf("tags"))
		require.NotEqual(t, tags.UnsafePointer(), otherTags.UnsafePointer())
		element := tags.Elem().FieldByName("ElemInfo")
		otherElement := otherTags.Elem().FieldByName("ElemInfo")
		require.NotEqual(t, element.UnsafePointer(), otherElement.UnsafePointer())
		require.NotEqual(t, element.Elem().FieldByName("Enum").UnsafePointer(), otherElement.Elem().FieldByName("Enum").UnsafePointer())
		return
	}
	first, err := a.ToJSONSchema()
	require.NoError(t, err)
	second, err := b.ToJSONSchema()
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.NotSame(t, first.Properties, second.Properties)
	query, ok := first.Properties.Get("query")
	require.True(t, ok)
	otherQuery, ok := second.Properties.Get("query")
	require.True(t, ok)
	require.NotSame(t, query, otherQuery)
	require.NotSame(t, query.Properties, otherQuery.Properties)
	tags, ok := query.Properties.Get("tags")
	require.True(t, ok)
	otherTags, ok := otherQuery.Properties.Get("tags")
	require.True(t, ok)
	require.NotSame(t, tags, otherTags)
	require.NotSame(t, tags.Items, otherTags.Items)
	require.NotEqual(t, reflect.ValueOf(tags.Items.Enum).UnsafePointer(), reflect.ValueOf(otherTags.Items.Enum).UnsafePointer())
	require.NotEqual(t, reflect.ValueOf(first.Required).UnsafePointer(), reflect.ValueOf(second.Required).UnsafePointer())
	require.NotEqual(t, reflect.ValueOf(first.Definitions).UnsafePointer(), reflect.ValueOf(second.Definitions).UnsafePointer())
	require.NotSame(t, first.Definitions["term"], second.Definitions["term"])
	require.NotSame(t, first.Definitions["term"].AnyOf[0], second.Definitions["term"].AnyOf[0])
	require.NotEqual(t, reflect.ValueOf(first.Default).UnsafePointer(), reflect.ValueOf(second.Default).UnsafePointer())
}

func TestSessionCaptureToolSearchSchemasCompleteAndDetach(t *testing.T) {
	for _, representation := range []string{"params", "jsonschema"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", representation, streaming), func(t *testing.T) {
				info, mutateOriginal := sessionCaptureToolDefinition(t, representation)
				requireSessionCaptureParamsRepresentation(t, info.ParamsOneOf, representation)
				info.Extra["runtime"] = sessionCaptureRuntimeMetadata{Values: []int{1, 2}}
				wantTool := sessionCaptureJSON(t, info)
				input := []*schema.Message{{Role: schema.User, Content: "input", UserInputMultiContent: []schema.MessageInputPart{{
					Type: schema.ChatMessagePartTypeToolSearchResult, ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{info}},
				}}}}
				final := &schema.Message{Role: schema.Assistant, Content: "final answer", AssistantGenMultiContent: []schema.MessageOutputPart{{
					Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "thinking", Signature: "signature"},
					StreamingMeta: &schema.MessageStreamingMeta{Index: 7}, Extra: map[string]any{"runtime": sessionCaptureRuntimeMetadata{Values: []int{3, 4}}},
				}}}
				var modelInput []*schema.Message
				mdl := &sessionCaptureTestModel{stream: func(_ context.Context, messages []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
					modelInput = messages
					return schema.StreamReaderFromArray([]*schema.Message{final}), nil
				}}
				capture := NewSessionCapture()
				agent := newSessionCaptureAgent(t, mdl, nil, 2)
				ctx := WithSessionCapture(t.Context(), capture)
				if streaming {
					reader, err := agent.Stream(ctx, input)
					require.NoError(t, err)
					chunks, err := readSessionCaptureStream(reader)
					require.NoError(t, err)
					require.Equal(t, "final answer", chunks[0].Content)
				} else {
					message, err := agent.Generate(ctx, input)
					require.NoError(t, err)
					require.Equal(t, "final answer", message.Content)
				}
				<-capture.Done()
				// Mutate the constructor-owned maps/pointers before the first Snapshot.
				mutateOriginal()
				info.Extra["runtime"].(sessionCaptureRuntimeMetadata).Values[0] = 99
				final.AssistantGenMultiContent[0].StreamingMeta.Index = 99
				final.AssistantGenMultiContent[0].Extra["runtime"].(sessionCaptureRuntimeMetadata).Values[0] = 99
				result := capture.Snapshot()
				require.NoError(t, result.Err, "a successful multimodal turn must remain archivable")
				require.True(t, result.Complete)
				require.Len(t, result.Messages, 1)
				captured := result.Input[0].UserInputMultiContent[0].ToolSearchResult.Tools[0]
				baseline := result.ModelInput[0].UserInputMultiContent[0].ToolSearchResult.Tools[0]
				loopTool := modelInput[0].UserInputMultiContent[0].ToolSearchResult.Tools[0]
				requireSessionCaptureParamsRepresentation(t, captured.ParamsOneOf, representation)
				requireSessionCaptureParamsRepresentation(t, baseline.ParamsOneOf, representation)
				requireSessionCaptureParamsRepresentation(t, loopTool.ParamsOneOf, representation)
				require.JSONEq(t, wantTool, sessionCaptureJSON(t, captured))
				require.JSONEq(t, wantTool, sessionCaptureJSON(t, baseline))
				require.JSONEq(t, wantTool, sessionCaptureJSON(t, loopTool))
				require.Equal(t, []int{1, 2}, captured.Extra["runtime"].(sessionCaptureRuntimeMetadata).Values)
				requireSessionCaptureParamsDetached(t, captured.ParamsOneOf, baseline.ParamsOneOf)
				requireSessionCaptureParamsDetached(t, captured.ParamsOneOf, loopTool.ParamsOneOf)
				part := result.Messages[0].AssistantGenMultiContent[0]
				require.Equal(t, 7, part.StreamingMeta.Index)
				require.Equal(t, []int{3, 4}, part.Extra["runtime"].(sessionCaptureRuntimeMetadata).Values)
				again := capture.Snapshot()
				require.NoError(t, again.Err)
				requireSessionCaptureParamsDetached(t, captured.ParamsOneOf, again.Input[0].UserInputMultiContent[0].ToolSearchResult.Tools[0].ParamsOneOf)
				if representation == "jsonschema" {
					definition, err := captured.ToJSONSchema()
					require.NoError(t, err)
					query, ok := definition.Properties.Get("query")
					require.True(t, ok)
					tags, ok := query.Properties.Get("tags")
					require.True(t, ok)
					tags.Items.Enum[0] = "snapshot-changed"
					query.Properties.Set("snapshot-added", &jsonschema.Schema{Type: "boolean"})
					definition.Required[0] = "snapshot-changed"
					definition.Definitions["term"].AnyOf[0].Type = "boolean"
					definition.Default.(map[string]any)["provider"].([]any)[0] = "snapshot-changed"
				}
				*captured.ParamsOneOf = *schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})
				captured.Extra["runtime"].(sessionCaptureRuntimeMetadata).Values[0] = 100
				part.StreamingMeta.Index = 100
				part.Extra["runtime"].(sessionCaptureRuntimeMetadata).Values[0] = 100
				after := capture.Snapshot()
				require.NoError(t, after.Err)
				require.True(t, after.Complete)
				require.Equal(t, again, after, "mutating a returned Snapshot must not affect another Snapshot")
			})
		}
	}
}

func TestSessionCaptureToolSearchStillRejectsUnknownRuntimeValues(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, test := range []struct {
		name  string
		value any
		err   error
	}{
		{"private fields", struct{ private []int }{private: []int{1}}, errSessionCaptureUnexportedField},
		{"private marshaler", sessionCapturePrivateMarshaler{private: []int{1}}, errSessionCaptureUnexportedField},
		{"cycle", cycle, errSessionCaptureCyclicValue},
		{"function", func() {}, errSessionCaptureUnsupportedValue},
	} {
		t.Run(test.name, func(t *testing.T) {
			info, _ := sessionCaptureToolDefinition(t, "params")
			info.Extra["unsupported"] = test.value
			capture := NewSessionCapture()
			mdl := &sessionCaptureTestModel{stream: func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("final answer", nil)}), nil
			}}
			agent := newSessionCaptureAgent(t, mdl, nil, 2)
			message, err := agent.Generate(WithSessionCapture(t.Context(), capture), []*schema.Message{{Role: schema.User, Content: "input", UserInputMultiContent: []schema.MessageInputPart{{
				Type: schema.ChatMessagePartTypeToolSearchResult, ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{info}},
			}}}})
			require.NoError(t, err)
			require.Equal(t, "final answer", message.Content)
			result := capture.Snapshot()
			require.ErrorIs(t, result.Err, test.err)
			require.False(t, result.Complete)
		})
	}
}

type sessionCapturePrivateMarshaler struct{ private []int }

func (sessionCapturePrivateMarshaler) MarshalJSON() ([]byte, error) {
	return []byte(`{"public":"would discard private state"}`), nil
}

func TestSessionCaptureToolSearchRejectsLossyJSONSchema(t *testing.T) {
	for _, test := range []struct {
		name       string
		definition *jsonschema.Schema
	}{
		{"SDK drops Extras", &jsonschema.Schema{Type: "object", Extras: map[string]any{"x-provider": "not restored by the locked SDK"}}},
		{"SDK rounds large integer", &jsonschema.Schema{Type: "integer", Default: int64(9007199254740993)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := &schema.ToolInfo{Name: "search", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(test.definition)}
			capture := NewSessionCapture()
			mdl := &sessionCaptureTestModel{stream: func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("final answer", nil)}), nil
			}}
			agent := newSessionCaptureAgent(t, mdl, nil, 2)
			message, err := agent.Generate(WithSessionCapture(t.Context(), capture), []*schema.Message{sessionCaptureMultimodalInput(info)})
			require.NoError(t, err)
			require.Equal(t, "final answer", message.Content)
			result := capture.Snapshot()
			require.ErrorIs(t, result.Err, errSessionCaptureUnsupportedValue)
			require.ErrorContains(t, result.Err, "parameter schema JSON round trip")
			require.False(t, result.Complete)
		})
	}
}
