package agentv3

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"csust-got/agent/session"
	"csust-got/orm"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestSessionCaptureToolSearchSchemasArchiveCommitAndLoad(t *testing.T) {
	for _, representation := range []string{"params", "jsonschema"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", representation, streaming), func(t *testing.T) {
				setupReplySessionRedis(t)
				service, err := orm.NewProductionAgentV3SessionService(t.TempDir(), session.Options{TTL: time.Hour})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, service.Close()) })
				info, _ := sessionCaptureToolDefinition(t, representation)
				requireSessionCaptureParamsRepresentation(t, info.ParamsOneOf, representation)
				input := []*schema.Message{schema.SystemMessage("system"), sessionCaptureMultimodalInput(info)}
				first := &schema.Message{Role: schema.Assistant, ReasoningContent: "tool reasoning", ToolCalls: []schema.ToolCall{lookupToolCall("call", `{"query":"长沙"}`)}}
				final := &schema.Message{Role: schema.Assistant, Content: "final answer", ReasoningContent: "final reasoning", AssistantGenMultiContent: []schema.MessageOutputPart{{
					Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "thinking", Signature: "signed-token"},
				}}, ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 12, CompletionTokens: 4, TotalTokens: 16}}}
				mdl := &scriptedToolModel{turns: [][]*schema.Message{{first}, {final}}}
				agent := newSessionCaptureAgent(t, mdl, []tool.BaseTool{echoLookupTool{}}, 4)
				capture := NewSessionCapture()
				ctx := WithSessionCapture(t.Context(), capture)
				if streaming {
					reader, err := agent.Stream(ctx, input)
					require.NoError(t, err)
					chunks, err := readSessionCaptureStream(reader)
					require.NoError(t, err)
					var visible string
					for _, chunk := range chunks {
						if isClearStreamOutputMessage(chunk) {
							visible = ""
							continue
						}
						visible += chunk.Content
					}
					require.Equal(t, "final answer", visible)
				} else {
					message, err := agent.Generate(ctx, input)
					require.NoError(t, err)
					require.Equal(t, "final answer", message.Content)
				}
				<-capture.Done()
				snapshot := capture.Snapshot()
				require.NoError(t, snapshot.Err, "a delivered successful turn must not fail capture on SDK parameter schemas")
				require.True(t, snapshot.Complete)
				require.Equal(t, input, snapshot.Input, "archive's reflect.DeepEqual baseline must include the original private representation")
				capturedTool := snapshot.Input[1].UserInputMultiContent[4].ToolSearchResult.Tools[0]
				requireSessionCaptureParamsRepresentation(t, capturedTool.ParamsOneOf, representation)
				requireSessionCaptureParamsDetached(t, info.ParamsOneOf, capturedTool.ParamsOneOf)
				baselineTool := snapshot.ModelInput[1].UserInputMultiContent[4].ToolSearchResult.Tools[0]
				requireSessionCaptureParamsRepresentation(t, baselineTool.ParamsOneOf, representation)
				requireSessionCaptureParamsDetached(t, capturedTool.ParamsOneOf, baselineTool.ParamsOneOf)
				require.Len(t, snapshot.Messages, 3)
				require.Equal(t, first, snapshot.Messages[0])
				require.Equal(t, final, snapshot.Messages[2])
				state := &agentV3SessionTurn{input: input, kinds: []agentV3SessionInputKind{agentV3SessionFrame, agentV3SessionCurrent}}
				archive, err := agentV3SessionArchive(state, snapshot)
				require.NoError(t, err)
				require.Len(t, archive.Delta, 4)
				if representation == "params" {
					normalized := capture.Snapshot()
					normalizedTool := normalized.Input[1].UserInputMultiContent[4].ToolSearchResult.Tools[0]
					definition, err := normalizedTool.ToJSONSchema()
					require.NoError(t, err)
					normalizedTool.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(definition)
					requireSessionCaptureParamsRepresentation(t, normalizedTool.ParamsOneOf, "jsonschema")
					originalDefinition, err := info.ToJSONSchema()
					require.NoError(t, err)
					require.JSONEq(t, sessionCaptureJSON(t, originalDefinition), sessionCaptureJSON(t, definition))
					_, err = agentV3SessionArchive(state, normalized)
					require.ErrorIs(t, err, errAgentV3SessionBaseline, "JSON-equivalent backing conversion must expose the archive integration risk")
				}
				runID, err := session.NewID()
				require.NoError(t, err)
				scope := session.Scope{Bot: "schema-capture", Platform: agentV3Platform, ChatID: -100}
				node, err := service.Commit(t.Context(), session.CommitRequest{
					Scope: scope, Agent: "schema-capture", RunID: runID, Capture: archive,
					Receipt: session.DeliveryReceipt{MessageIDs: []int{42}},
				})
				require.NoError(t, err)
				selection := session.Selection{Scope: scope, Mode: session.SelectReply, ReplyMessageID: 42}
				loaded, err := service.Load(t.Context(), selection)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, loaded.Parent.Close()) })
				require.Equal(t, node.Ref, loaded.Parent.Ref())
				want := append([]*schema.Message{input[1]}, snapshot.Messages...)
				require.Len(t, loaded.Messages, len(want))
				require.JSONEq(t, sessionCaptureJSON(t, want), sessionCaptureJSON(t, loaded.Messages))
				replayedTool := loaded.Messages[0].UserInputMultiContent[4].ToolSearchResult.Tools[0]
				requireSessionCaptureParamsRepresentation(t, replayedTool.ParamsOneOf, representation)
				requireSessionCaptureParamsDetached(t, capturedTool.ParamsOneOf, replayedTool.ParamsOneOf)
				require.JSONEq(t, sessionCaptureJSON(t, info), sessionCaptureJSON(t, replayedTool))
				definition, err := replayedTool.ToJSONSchema()
				require.NoError(t, err)
				originalDefinition, err := info.ToJSONSchema()
				require.NoError(t, err)
				require.JSONEq(t, sessionCaptureJSON(t, originalDefinition), sessionCaptureJSON(t, definition))
				require.Equal(t, "call", loaded.Messages[2].ToolCallID)
				require.Equal(t, "signed-token", loaded.Messages[3].AssistantGenMultiContent[0].Reasoning.Signature)
				require.Equal(t, 16, loaded.Messages[3].ResponseMeta.Usage.TotalTokens)
			})
		}
	}
}

func TestSessionCaptureDirectJSONSchemaNumbersArchiveCommitAndLoad(t *testing.T) {
	for _, test := range []struct {
		name       string
		definition func() *jsonschema.Schema
	}{
		{"int", func() *jsonschema.Schema {
			return &jsonschema.Schema{Type: "integer", Default: 1, Enum: []any{1, 2}}
		}},
		{"json.Number", func() *jsonschema.Schema {
			return &jsonschema.Schema{Type: "integer", Default: json.Number("1"), Enum: []any{json.Number("1"), json.Number("2")}}
		}},
		{"float64", func() *jsonschema.Schema {
			return &jsonschema.Schema{Type: "number", Default: 1.25, Enum: []any{1.25, 2.5}}
		}},
		{"nested maps", func() *jsonschema.Schema {
			return &jsonschema.Schema{Type: "object",
				Default: map[string]any{"count": 1, "weights": []any{json.Number("2"), 3.5}},
				Enum:    []any{map[string]any{"count": 2, "weights": []any{json.Number("4"), 5.5}}},
			}
		}},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, streaming), func(t *testing.T) {
				setupReplySessionRedis(t)
				service, err := orm.NewProductionAgentV3SessionService(t.TempDir(), session.Options{TTL: time.Hour})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, service.Close()) })
				definition := test.definition()
				info := &schema.ToolInfo{Name: "numeric", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(definition), Extra: map[string]any{"typed_number": 1}}
				input := []*schema.Message{schema.SystemMessage("system"), sessionCaptureMultimodalInput(info)}
				input[1].Extra = map[string]any{"typed_number": 1, "large_integer": int64(9007199254740993), "runtime": sessionCaptureRuntimeMetadata{Values: []int{7}}}
				final := schema.AssistantMessage("final answer", nil)
				want := sessionCaptureJSON(t, []*schema.Message{input[1], final})
				wantTool := sessionCaptureJSON(t, info)
				mdl := &scriptedToolModel{turns: [][]*schema.Message{{final}}}
				agent := newSessionCaptureAgent(t, mdl, nil, 2)
				capture := NewSessionCapture()
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
				snapshot := capture.Snapshot()
				require.NoError(t, snapshot.Err)
				require.True(t, snapshot.Complete)
				capturedTool := snapshot.Input[1].UserInputMultiContent[4].ToolSearchResult.Tools[0]
				requireSessionCaptureParamsRepresentation(t, capturedTool.ParamsOneOf, "jsonschema")
				require.JSONEq(t, wantTool, sessionCaptureJSON(t, capturedTool))
				capturedDefinition, err := capturedTool.ToJSONSchema()
				require.NoError(t, err)
				require.NotSame(t, definition, capturedDefinition)
				require.Equal(t, 1, capturedTool.Extra["typed_number"])
				require.Equal(t, input[1].Extra, snapshot.Input[1].Extra, "non-schema Go runtime values must not be normalized")
				t.Logf("original Default=%T, captured Default=%T, Complete=%t", definition.Default, capturedDefinition.Default, snapshot.Complete)
				state := &agentV3SessionTurn{input: input, kinds: []agentV3SessionInputKind{agentV3SessionFrame, agentV3SessionCurrent}}
				archive, err := agentV3SessionArchive(state, snapshot)
				require.NoError(t, err, "SDK numeric normalization must not reject an unchanged invocation baseline")
				for _, change := range []struct {
					name   string
					mutate func(*schema.Message)
				}{
					{"text", func(message *schema.Message) { message.Content = "changed" }},
					{"message numeric Go type", func(message *schema.Message) { message.Extra["typed_number"] = float64(1) }},
					{"tool numeric Go type", func(message *schema.Message) {
						message.UserInputMultiContent[4].ToolSearchResult.Tools[0].Extra["typed_number"] = float64(1)
					}},
					{"non-JSON runtime field", func(message *schema.Message) { message.Extra["runtime"].(sessionCaptureRuntimeMetadata).Values[0] = 99 }},
					{"schema value", func(message *schema.Message) {
						changedDefinition, err := message.UserInputMultiContent[4].ToolSearchResult.Tools[0].ToJSONSchema()
						require.NoError(t, err)
						changedDefinition.Default = 99
					}},
				} {
					t.Run("reject changed "+change.name, func(t *testing.T) {
						changedInput, err := cloneSessionCaptureMessages(input)
						require.NoError(t, err)
						change.mutate(changedInput[1])
						changedState := &agentV3SessionTurn{input: changedInput, kinds: state.kinds}
						_, err = agentV3SessionArchive(changedState, snapshot)
						require.ErrorIs(t, err, errAgentV3SessionBaseline)
					})
				}
				runID, err := session.NewID()
				require.NoError(t, err)
				scope := session.Scope{Bot: "numeric-schema-capture", Platform: agentV3Platform, ChatID: -100}
				_, err = service.Commit(t.Context(), session.CommitRequest{
					Scope: scope, Agent: "numeric-schema-capture", RunID: runID, Capture: archive,
					Receipt: session.DeliveryReceipt{MessageIDs: []int{42}},
				})
				require.NoError(t, err)
				if original, ok := definition.Default.(map[string]any); ok {
					original["count"] = 99
					definition.Enum[0].(map[string]any)["count"] = 99
					capturedDefinition.Default.(map[string]any)["count"] = 100
					capturedDefinition.Enum[0].(map[string]any)["count"] = 100
				} else {
					definition.Default, definition.Enum[0] = 99, 99
					capturedDefinition.Default, capturedDefinition.Enum[0] = 100, 100
				}
				fresh := capture.Snapshot()
				require.NoError(t, fresh.Err)
				require.True(t, fresh.Complete)
				require.JSONEq(t, wantTool, sessionCaptureJSON(t, fresh.Input[1].UserInputMultiContent[4].ToolSearchResult.Tools[0]))
				_, err = agentV3SessionArchive(state, fresh)
				require.ErrorIs(t, err, errAgentV3SessionBaseline, "mutating the original schema is a real baseline change")
				loaded, err := service.Load(t.Context(), session.Selection{Scope: scope, Mode: session.SelectReply, ReplyMessageID: 42})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, loaded.Parent.Close()) })
				require.JSONEq(t, want, sessionCaptureJSON(t, loaded.Messages))
				require.Equal(t, json.Number("9007199254740993"), loaded.Messages[0].Extra["large_integer"])
				replayedTool := loaded.Messages[0].UserInputMultiContent[4].ToolSearchResult.Tools[0]
				requireSessionCaptureParamsRepresentation(t, replayedTool.ParamsOneOf, "jsonschema")
				require.JSONEq(t, wantTool, sessionCaptureJSON(t, replayedTool))
			})
		}
	}
}

func sessionCaptureMultimodalInput(info *schema.ToolInfo) *schema.Message {
	imageURL, audioData, videoURL, fileData := "https://example.invalid/image", "AQID", "https://example.invalid/video", "AQID"
	return &schema.Message{Role: schema.User, Content: "multimodal input", UserInputMultiContent: []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &imageURL, MIMEType: "image/png"}, Detail: schema.ImageURLDetailHigh}},
		{Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{Base64Data: &audioData, MIMEType: "audio/wav"}}},
		{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{URL: &videoURL, MIMEType: "video/mp4"}}},
		{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{MessagePartCommon: schema.MessagePartCommon{Base64Data: &fileData, MIMEType: "application/pdf"}, Name: "doc.pdf"}},
		{Type: schema.ChatMessagePartTypeToolSearchResult, ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{info}}},
	}}
}
