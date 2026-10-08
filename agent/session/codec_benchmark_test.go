package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

func codecBenchmarkFixtures() map[string]TurnCapture {
	text := TurnCapture{Delta: History(schema.UserMessage("question"), schema.AssistantMessage("answer", nil)), Complete: true}
	large := TurnCapture{Delta: History(schema.UserMessage("question"), schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "lookup", Arguments: `{"q":"campus"}`}}}), schema.ToolMessage(strings.Repeat("tool result 长沙\n", 20000), "call"), schema.AssistantMessage("answer", nil)), Complete: true}
	payload := strings.Repeat("A", 1<<20)
	media := TurnCapture{Delta: History(&schema.Message{Role: schema.User, Content: "image", UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &payload}}}}}, schema.AssistantMessage("answer", nil)), Complete: true}
	fixtures := map[string]TurnCapture{"text": text, "largeTools": large, "media": media}
	for _, backing := range []string{"params", "jsonschema"} {
		info := &schema.ToolInfo{Name: "lookup", Desc: "Search the campus and course catalog", Extra: map[string]any{"integer": 1}}
		if backing == "params" {
			info.ParamsOneOf = schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"query": {Type: schema.String, Desc: "Full query text", Required: true}})
		} else {
			info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{Type: "integer", Default: json.Number("1"), Enum: []any{json.Number("1"), json.Number("2")}})
		}
		fixtures[backing] = TurnCapture{Delta: History(&schema.Message{Role: schema.User, Content: "search", UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeToolSearchResult, ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{info}}}}}, schema.AssistantMessage("answer", nil)), Complete: true}
	}
	return fixtures
}

func BenchmarkSessionCodecSnapshot(b *testing.B) {
	for name, fixture := range codecBenchmarkFixtures() {
		b.Run(name, func(b *testing.B) {
			encoded, err := json.Marshal(fixture)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(len(encoded)), "fixture-bytes")
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := Snapshot(fixture); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(encoded)), "fixture-bytes")
		})
	}
}
