package agentv3

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"csust-got/agent/session"
	"csust-got/orm"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type sessionPipelineFixture struct {
	input []*schema.Message
	tools []tool.BaseTool
}

type sessionPipelineResultTool struct {
	lookupTool
	result string
}

func (t sessionPipelineResultTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return t.result, nil
}

func sessionPipelineFixtures() map[string]sessionPipelineFixture {
	base := func(current *schema.Message) []*schema.Message {
		return []*schema.Message{schema.SystemMessage("system"), schema.UserMessage("fallback question"), schema.AssistantMessage("fallback answer", nil), current}
	}
	fixtures := map[string]sessionPipelineFixture{
		"text":       {input: base(schema.UserMessage("current input"))},
		"largeTools": {input: base(schema.UserMessage("current input")), tools: []tool.BaseTool{sessionPipelineResultTool{result: strings.Repeat("tool result 长沙\n", 20000)}}},
		"media":      {input: base(sessionEstimateMedia(schema.ChatMessagePartTypeImageURL, strings.Repeat("A", 1<<20), false))},
	}
	for _, backing := range []string{"params", "jsonschema"} {
		info := &schema.ToolInfo{Name: "lookup", Desc: "Query the campus catalog"}
		if backing == "params" {
			info.ParamsOneOf = schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"q": {Type: schema.String, Desc: "query", Required: true}})
		} else {
			info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{Type: "integer", Default: json.Number("1"), Enum: []any{json.Number("1"), json.Number("2")}})
		}
		current := &schema.Message{Role: schema.User, Content: "current input", Extra: map[string]any{"integer": int64(9007199254740993)}, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeToolSearchResult, ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{info}}}}}
		fixtures[backing] = sessionPipelineFixture{input: base(current)}
	}
	return fixtures
}

func newSessionPipelineAgent(ctx context.Context, fixture sessionPipelineFixture) (*CustomAgent, error) {
	mdl := &sessionCaptureTestModel{stream: func(_ context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		if len(fixture.tools) > 0 {
			for _, message := range input {
				if message.Role == schema.Tool {
					return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("final answer", nil)}), nil
				}
			}
			return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("call", `{"q":"campus"}`)}}}), nil
		}
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("final answer", nil)}), nil
	}}
	return NewCustomAgent(ctx, &CustomAgentConfig{Name: "pipeline", Model: mdl, Tools: fixture.tools, MaxSteps: 4})
}

func TestSessionCaptureCodecFixturesArchiveCommitLoad(t *testing.T) {
	for name, fixture := range sessionPipelineFixtures() {
		t.Run(name, func(t *testing.T) {
			f := newAgentSessionFixture(t)
			a, err := newSessionPipelineAgent(t.Context(), fixture)
			require.NoError(t, err)
			capture := NewSessionCapture()
			_, err = a.Generate(WithSessionCapture(t.Context(), capture), fixture.input)
			require.NoError(t, err)
			snapshot := capture.Snapshot()
			require.NoError(t, snapshot.Err)
			require.True(t, snapshot.Complete)
			archive, err := agentV3SessionArchive(&agentV3SessionTurn{input: fixture.input, kinds: []agentV3SessionInputKind{agentV3SessionFrame, agentV3SessionHistory, agentV3SessionHistory, agentV3SessionCurrent}}, snapshot)
			require.NoError(t, err)
			require.Len(t, archive.Bootstrap, 2)
			runID, err := session.NewID()
			require.NoError(t, err)
			_, err = f.service.Commit(t.Context(), session.CommitRequest{Scope: f.scope(), Agent: "pipeline", RunID: runID, Capture: archive, Receipt: session.DeliveryReceipt{MessageIDs: []int{88}}})
			require.NoError(t, err)
			loaded, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: 88})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, loaded.Parent.Close()) })
			want := append(append([]*schema.Message{}, fixture.input[1:]...), snapshot.Messages...)
			require.JSONEq(t, sessionCaptureJSON(t, want), sessionCaptureJSON(t, loaded.Messages))
			if name == "params" || name == "jsonschema" {
				require.Equal(t, json.Number("9007199254740993"), loaded.Messages[2].Extra["integer"])
			}
			loaded.Messages[2].Content = "mutated loaded"
			snapshot.Input[3].Content = "mutated snapshot"
			require.NotEqual(t, "mutated loaded", fixture.input[3].Content)
			require.NotEqual(t, "mutated snapshot", capture.Snapshot().Input[3].Content)
		})
	}
}

func BenchmarkSessionCaptureArchiveCommitLoad(b *testing.B) {
	for name, fixture := range sessionPipelineFixtures() {
		b.Run(name, func(b *testing.B) {
			mini, err := miniredis.Run()
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(mini.Close)
			client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
			b.Cleanup(func() { _ = client.Close() })
			repo, err := orm.NewAgentV3SessionRepository(client, "codec-benchmark:")
			if err != nil {
				b.Fatal(err)
			}
			files, err := session.NewFileStore(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			service, err := session.NewService(repo, files, session.Options{TTL: time.Hour})
			if err != nil {
				_ = files.Close()
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := service.Close(); err != nil {
					b.Error(err)
				}
			})
			a, err := newSessionPipelineAgent(b.Context(), fixture)
			if err != nil {
				b.Fatal(err)
			}
			state := &agentV3SessionTurn{input: fixture.input, kinds: []agentV3SessionInputKind{agentV3SessionFrame, agentV3SessionHistory, agentV3SessionHistory, agentV3SessionCurrent}}
			b.ReportAllocs()
			var i int64
			for b.Loop() {
				i++
				capture := NewSessionCapture()
				if _, err := a.Generate(WithSessionCapture(b.Context(), capture), fixture.input); err != nil {
					b.Fatal(err)
				}
				archive, err := agentV3SessionArchive(state, capture.Snapshot())
				if err != nil {
					b.Fatal(err)
				}
				runID, err := session.NewID()
				if err != nil {
					b.Fatal(err)
				}
				scope := session.Scope{Namespace: repo.Namespace(), Bot: "pipeline", Platform: "tg", ChatID: i}
				_, err = service.Commit(b.Context(), session.CommitRequest{Scope: scope, Agent: "pipeline", RunID: runID, Capture: archive, Receipt: session.DeliveryReceipt{MessageIDs: []int{88}}})
				if err != nil {
					b.Fatal(err)
				}
				loaded, err := service.Load(b.Context(), session.Selection{Scope: scope, Mode: session.SelectReply, ReplyMessageID: 88})
				if err != nil {
					b.Fatal(err)
				}
				if len(loaded.Messages) != len(fixture.input)-1+len(archive.Delta)-1 {
					b.Fatalf("unexpected replay length: %d", len(loaded.Messages))
				}
				if err := loaded.Parent.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
