package agentv3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"csust-got/config"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionCaptureFullToolHistory(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			url := "data:image/png;base64,aW1hZ2U="
			index := 0
			firstCall := lookupToolCall("first-a", `{"q":"first-a"}`)
			firstCall.Index = &index
			firstCall.Extra = map[string]any{"signature": []byte{1, 2, 3}}
			first := &schema.Message{
				Role: schema.Assistant, Content: "intermediate", ReasoningContent: "tool reasoning",
				ToolCalls: []schema.ToolCall{firstCall, lookupToolCall("first-b", `{"q":"first-b"}`)},
				Extra:     map[string]any{"typed": []int{1, 2}},
			}
			final := &schema.Message{
				Role: schema.Assistant, Content: "final answer", ReasoningContent: "final reasoning",
				Extra:        map[string]any{"nested": map[string]any{"count": int64(9)}},
				ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 11, CompletionTokens: 7}},
				AssistantGenMultiContent: []schema.MessageOutputPart{{
					Type:          schema.ChatMessagePartTypeReasoning,
					Reasoning:     &schema.MessageOutputReasoning{Text: "reasoning part", Signature: "encrypted signature"},
					StreamingMeta: &schema.MessageStreamingMeta{Index: 2},
				}},
			}
			mdl := &scriptedToolModel{turns: [][]*schema.Message{
				{first},
				{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
					lookupToolCall("second-a", `{"q":"second-a"}`), lookupToolCall("second-b", `{"q":"second-b"}`),
				}}},
				{final},
			}}
			capture := NewSessionCapture()
			ctx := WithSessionCapture(t.Context(), capture)
			agent := newSessionCaptureAgent(t, mdl, []tool.BaseTool{echoLookupTool{}}, 12)
			input := []*schema.Message{
				schema.SystemMessage("system"),
				{Role: schema.User, Content: "current input", UserInputMultiContent: []schema.MessageInputPart{{
					Type:  schema.ChatMessagePartTypeImageURL,
					Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}},
				}}},
			}
			if streaming {
				reader, err := agent.Stream(ctx, input)
				require.NoError(t, err)
				chunks, err := readSessionCaptureStream(reader)
				require.NoError(t, err)
				assert.True(t, capture.Snapshot().Complete, "Complete must already be visible at EOF")
				var visible string
				for _, chunk := range chunks {
					if isClearStreamOutputMessage(chunk) {
						visible = ""
						continue
					}
					assert.Equal(t, schema.Assistant, chunk.Role)
					assert.Empty(t, chunk.ToolCalls)
					assert.Nil(t, chunk.ResponseMeta)
					visible += chunk.Content
				}
				assert.Equal(t, "final answer", visible)
			} else {
				message, err := agent.Generate(ctx, input)
				require.NoError(t, err)
				assert.Equal(t, "final answer", message.Content)
				assert.Empty(t, message.ToolCalls)
			}
			result := capture.Snapshot()
			require.NoError(t, result.Err)
			require.True(t, result.Complete)
			require.Len(t, result.Messages, 7)
			assert.Equal(t, input, result.Input)
			expectedFirst, err := schema.ConcatMessages([]*schema.Message{first})
			require.NoError(t, err)
			expectedFinal, err := schema.ConcatMessages([]*schema.Message{final})
			require.NoError(t, err)
			assert.Equal(t, expectedFirst, result.Messages[0])
			assert.Equal(t, expectedFinal, result.Messages[6])
			assertLookupToolResultPair(t, result.Messages, 0, "first-a", `{"q":"first-a"}`)
			assertLookupToolResultPair(t, result.Messages, 0, "first-b", `{"q":"first-b"}`)
			assertLookupToolResultPair(t, result.Messages, 3, "second-a", `{"q":"second-a"}`)
			assertLookupToolResultPair(t, result.Messages, 3, "second-b", `{"q":"second-b"}`)
			require.Len(t, result.Guidance, 1)
			assert.Equal(t, 6, result.Guidance[0].BeforeMessage)
			assert.Contains(t, result.Guidance[0].Message.Content, "已经进行了 2 轮工具调用")
			assert.Contains(t, result.ModelInput[0].Content, loopDirectiveText)
			assert.Equal(t, mdl.capturedInputs()[0][0].Content, result.ModelInput[0].Content)
			index = 99
			first.Extra["typed"].([]int)[0] = 99
			firstCall.Extra["signature"].([]byte)[0] = 99
			final.ResponseMeta.Usage.PromptTokens = 99
			final.AssistantGenMultiContent[0].Reasoning.Signature = "changed signature"
			assert.Equal(t, result.Messages, capture.Snapshot().Messages, "model-owned messages must not alias the archive")
			select {
			case <-capture.Done():
			default:
				t.Fatal("Done must close before stream EOF")
			}
		})
	}
}

func TestSessionCaptureNormalLastStepCompletes(t *testing.T) {
	for _, maxSteps := range []int{1, 2} {
		t.Run(fmt.Sprintf("maxSteps=%d", maxSteps), func(t *testing.T) {
			turns := [][]*schema.Message{{schema.AssistantMessage("final answer", nil)}}
			if maxSteps == 2 {
				turns = append([][]*schema.Message{{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("call", `{}`)}}}}, turns...)
			}
			agent := newSessionCaptureAgent(t, &scriptedToolModel{turns: turns}, []tool.BaseTool{lookupTool{}}, maxSteps)
			capture := NewSessionCapture()
			message, err := agent.Generate(WithSessionCapture(t.Context(), capture), []*schema.Message{schema.UserMessage("input")})
			require.NoError(t, err)
			assert.Equal(t, "final answer", message.Content)
			result := capture.Snapshot()
			require.NoError(t, result.Err)
			require.True(t, result.Complete)
			require.Len(t, result.Guidance, 1)
			assert.Contains(t, result.Guidance[0].Message.Content, finalTurnGuidance)
			assert.Equal(t, len(result.Messages)-1, result.Guidance[0].BeforeMessage)
		})
	}
}

func TestSessionCaptureInvocationAndResultSnapshotsAreDetached(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	mdl := &sessionCaptureTestModel{stream: func(_ context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		close(started)
		<-release
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(input[0].Content, nil)}), nil
	}}
	agent := newSessionCaptureAgent(t, mdl, nil, 2)
	capture := NewSessionCapture()
	url := "original image"
	input := []*schema.Message{{
		Role: schema.User, Content: "original", Extra: map[string]any{"values": []int{1, 2}},
		UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}},
		}},
	}}
	reader, err := agent.Stream(WithSessionCapture(t.Context(), capture), input)
	require.NoError(t, err)
	<-started
	input[0].Content = "changed"
	input[0].Extra["values"].([]int)[0] = 99
	url = "changed image"
	input[0] = schema.UserMessage("replaced")
	close(release)
	_, err = readSessionCaptureStream(reader)
	require.NoError(t, err)
	result := capture.Snapshot()
	require.NoError(t, result.Err)
	require.True(t, result.Complete)
	assert.Equal(t, "original", result.Input[0].Content)
	assert.Equal(t, []int{1, 2}, result.Input[0].Extra["values"])
	assert.Equal(t, "original image", *result.Input[0].UserInputMultiContent[0].Image.URL)
	assert.Equal(t, "original", result.ModelInput[0].Content)
	assert.Equal(t, "original", result.Messages[0].Content)
	result.Input[0].Extra["values"].([]int)[0] = 100
	*result.ModelInput[0].UserInputMultiContent[0].Image.URL = "snapshot changed"
	result.Messages[0].Content = "snapshot changed"
	again := capture.Snapshot()
	assert.Equal(t, []int{1, 2}, again.Input[0].Extra["values"])
	assert.Equal(t, "original image", *again.ModelInput[0].UserInputMultiContent[0].Image.URL)
	assert.Equal(t, "original", again.Messages[0].Content)
}

func TestSessionCaptureIncompleteExits(t *testing.T) {
	call := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("call", `{}`)}}
	tests := []struct {
		name     string
		steps    []retryStreamStep
		maxSteps int
		wantErr  error
		calls    int
		messages int
	}{
		{"initial error", []retryStreamStep{{err: errAgentFailureUnderTest}}, 4, errAgentFailureUnderTest, 0, 0},
		{"partial error", []retryStreamStep{{chunks: []*schema.Message{schema.AssistantMessage("partial", nil)}, recvErr: errAgentFailureUnderTest}}, 4, errAgentFailureUnderTest, 0, 0},
		{"error after tool", []retryStreamStep{{chunks: []*schema.Message{call}}, {err: errAgentFailureUnderTest}}, 4, errAgentFailureUnderTest, 1, 2},
		{"empty stream", []retryStreamStep{{}}, 4, nil, 0, 0},
		{"nil response", []retryStreamStep{{chunks: []*schema.Message{nil}}}, 4, nil, 0, 0},
		{"empty assistant", []retryStreamStep{{chunks: []*schema.Message{schema.AssistantMessage("", nil)}}}, 4, nil, 0, 1},
		{"metadata only", []retryStreamStep{{chunks: []*schema.Message{{Role: schema.Assistant, Extra: map[string]any{"metadata": true}}}}}, 4, nil, 0, 1},
		{"truncated final", []retryStreamStep{{chunks: []*schema.Message{{Role: schema.Assistant, Content: "truncated", ResponseMeta: &schema.ResponseMeta{FinishReason: "length"}}}}}, 4, nil, 0, 1},
		{"filtered final", []retryStreamStep{{chunks: []*schema.Message{{Role: schema.Assistant, Content: "filtered", ResponseMeta: &schema.ResponseMeta{FinishReason: "content_filter"}}}}}, 4, nil, 0, 1},
		{"wrong role", []retryStreamStep{{chunks: []*schema.Message{schema.UserMessage("not assistant")}}}, 4, nil, 0, 1},
		{"last step tool call", []retryStreamStep{{chunks: []*schema.Message{call}}}, 1, nil, 0, 1},
		{"last step after executed tool", []retryStreamStep{{chunks: []*schema.Message{call}}, {chunks: []*schema.Message{call}}}, 2, nil, 1, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counting := &countingLookupTool{}
			mdl := &retryStubModel{streamSteps: tt.steps}
			agent := newSessionCaptureAgent(t, mdl, []tool.BaseTool{counting}, tt.maxSteps)
			capture := NewSessionCapture()
			_, err := agent.Generate(WithSessionCapture(t.Context(), capture), []*schema.Message{schema.UserMessage("input")})
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
			<-capture.Done()
			result := capture.Snapshot()
			require.NoError(t, result.Err)
			assert.False(t, result.Complete)
			assert.Len(t, result.Messages, tt.messages)
			assert.Equal(t, tt.calls, counting.callCount())
		})
	}
}

func TestSessionCaptureCancellation(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprintf("during=%v", during), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			mdl := &sessionCaptureTestModel{stream: func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				cancel()
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("final", nil)}), nil
			}}
			if !during {
				cancel()
			}
			capture := NewSessionCapture()
			agent := newSessionCaptureAgent(t, mdl, nil, 4)
			_, err := agent.Generate(WithSessionCapture(ctx, capture), []*schema.Message{schema.UserMessage("input")})
			if !during {
				require.ErrorIs(t, err, context.Canceled)
			}
			<-capture.Done()
			assert.False(t, capture.Snapshot().Complete)
		})
	}
}

func TestSessionCaptureDownstreamCloseAfterLastContent(t *testing.T) {
	for _, reasoning := range []string{"", "reasoning"} {
		t.Run(fmt.Sprintf("reasoning=%q", reasoning), func(t *testing.T) {
			release := make(chan struct{})
			mdl := &sessionCaptureTestModel{stream: func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				reader, writer := schema.Pipe[*schema.Message](0)
				go func() {
					defer writer.Close()
					writer.Send(&schema.Message{Role: schema.Assistant, Content: "final", ReasoningContent: reasoning}, nil)
					<-release
				}()
				return reader, nil
			}}
			capture := NewSessionCapture()
			agent := newSessionCaptureAgent(t, mdl, nil, 4)
			reader, err := agent.Stream(WithSessionCapture(t.Context(), capture), []*schema.Message{schema.UserMessage("input")})
			require.NoError(t, err)
			chunk, err := reader.Recv()
			require.NoError(t, err)
			assert.Equal(t, "final", chunk.Content)
			assert.False(t, capture.Snapshot().Complete)
			reader.Close()
			close(release)
			select {
			case <-capture.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("closed downstream must release loop")
			}
			result := capture.Snapshot()
			require.NoError(t, result.Err)
			assert.False(t, result.Complete)
			require.Len(t, result.Messages, 1)
			assert.Equal(t, reasoning, result.Messages[0].ReasoningContent)
		})
	}
}

func TestSessionCaptureSuccessfulRetryDiscardsFailedAttempt(t *testing.T) {
	stub := &retryStubModel{streamSteps: []retryStreamStep{
		{err: errRetryStubUpstream500},
		{chunks: []*schema.Message{{Role: schema.Assistant, Content: "failed partial", ToolCalls: []schema.ToolCall{lookupToolCall("failed", `{}`)}}}, recvErr: errRetryStubUpstream500},
		{chunks: []*schema.Message{{Role: schema.Assistant, Content: "successful tool turn", ToolCalls: []schema.ToolCall{lookupToolCall("successful", `{}`)}}}},
		{chunks: []*schema.Message{schema.AssistantMessage("failed final", nil)}, recvErr: errRetryStubUpstream500},
		{chunks: []*schema.Message{schema.AssistantMessage("successful final", nil)}},
	}}
	mdl := &retryingChatModel{inner: stub, retries: 2, sleep: func(context.Context, time.Duration) error { return nil }}
	counting := &countingLookupTool{}
	agent := newSessionCaptureAgent(t, mdl, []tool.BaseTool{counting}, 4)
	capture := NewSessionCapture()
	message, err := agent.Generate(WithSessionCapture(t.Context(), capture), []*schema.Message{schema.UserMessage("input")})
	require.NoError(t, err)
	assert.Equal(t, "successful final", message.Content)
	result := capture.Snapshot()
	require.NoError(t, result.Err)
	require.True(t, result.Complete)
	require.Len(t, result.Messages, 3)
	assert.Equal(t, "successful", result.Messages[0].ToolCalls[0].ID)
	assert.Equal(t, "successful", result.Messages[1].ToolCallID)
	assert.Equal(t, "successful final", result.Messages[2].Content)
	assert.Equal(t, 1, counting.callCount())
	assert.Equal(t, 5, stub.streamCalls)
}

func TestSessionCaptureToolErrorsCanComplete(t *testing.T) {
	for _, name := range []string{"lookup", "missing"} {
		t.Run(name, func(t *testing.T) {
			call := lookupToolCall("call", `{}`)
			call.Function.Name = name
			mdl := &scriptedToolModel{turns: [][]*schema.Message{
				{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call}}},
				{schema.AssistantMessage("recovered answer", nil)},
			}}
			agent := newSessionCaptureAgent(t, mdl, []tool.BaseTool{sessionCaptureFailingTool{}}, 4)
			capture := NewSessionCapture()
			_, err := agent.Generate(WithSessionCapture(t.Context(), capture), []*schema.Message{schema.UserMessage("input")})
			require.NoError(t, err)
			result := capture.Snapshot()
			require.NoError(t, result.Err)
			require.True(t, result.Complete)
			require.Len(t, result.Messages, 3)
			assert.Equal(t, "call", result.Messages[1].ToolCallID)
			assert.Equal(t, name, result.Messages[1].ToolName)
			assert.Contains(t, result.Messages[1].Content, "[Tool Error]")
		})
	}
}

func TestSessionCaptureAncestorToolCallsDoNotConsumeBudget(t *testing.T) {
	input := make([]*schema.Message, 0, 41)
	for i := range 20 {
		id := fmt.Sprintf("ancestor-%d", i)
		input = append(input,
			&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall(id, `{}`)}},
			schema.ToolMessage("ancestor result", id, schema.WithToolName("lookup")),
		)
	}
	input = append(input, schema.UserMessage("<agent_runtime_guidance>user-owned lookalike</agent_runtime_guidance>"))
	mdl := &scriptedToolModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("current-1", `{}`)}}},
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("current-2", `{}`)}}},
		{schema.AssistantMessage("final", nil)},
	}}
	agent := newSessionCaptureAgent(t, mdl, []tool.BaseTool{lookupTool{}}, 12)
	capture := NewSessionCapture()
	_, err := agent.Generate(WithSessionCapture(t.Context(), capture), input)
	require.NoError(t, err)
	inputs := mdl.capturedInputs()
	require.Len(t, inputs, 3)
	for _, round := range inputs[:2] {
		for _, message := range round {
			assert.NotContains(t, message.Content, finalTurnGuidance)
			assert.NotContains(t, message.Content, "停止重复调用")
		}
	}
	assert.Contains(t, inputs[2][len(inputs[2])-1].Content, "已经进行了 2 轮工具调用")
	assert.NotContains(t, inputs[2][len(inputs[2])-1].Content, finalTurnGuidance)
	result := capture.Snapshot()
	require.NoError(t, result.Err)
	require.True(t, result.Complete)
	assert.Len(t, result.Input, 41)
	assert.Len(t, result.Messages, 5)
	require.Len(t, result.Guidance, 1)
	assert.Equal(t, 4, result.Guidance[0].BeforeMessage)
	assert.Contains(t, result.Input[len(result.Input)-1].Content, "user-owned lookalike")
}

func TestSessionCaptureConcurrentSharedCompiledAgent(t *testing.T) {
	mdl := &sessionCaptureTestModel{stream: func(_ context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		var request string
		for _, message := range input {
			if message.Role == schema.User && strings.HasPrefix(message.Content, "request-") {
				request = message.Content
			}
		}
		for _, message := range input {
			if message.Role == schema.Tool {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("final "+request, nil)}), nil
			}
		}
		return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall(request, `{}`)}}}), nil
	}}
	compiled := &CompiledAgent{Agent: newSessionCaptureAgent(t, mdl, []tool.BaseTool{lookupTool{}}, 4)}
	type invocationResult struct {
		request string
		capture SessionCaptureResult
		err     error
	}
	results := make(chan invocationResult, 16)
	var workers sync.WaitGroup
	for i := range cap(results) {
		workers.Go(func() {
			request := fmt.Sprintf("request-%d", i)
			capture := NewSessionCapture()
			_, err := compiled.Agent.Generate(WithSessionCapture(t.Context(), capture), []*schema.Message{schema.UserMessage(request)})
			results <- invocationResult{request: request, capture: capture.Snapshot(), err: err}
		})
	}
	workers.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		require.NoError(t, result.capture.Err)
		require.True(t, result.capture.Complete)
		require.Len(t, result.capture.Input, 1)
		require.Len(t, result.capture.Messages, 3)
		assert.Equal(t, result.request, result.capture.Input[0].Content)
		assert.Equal(t, result.request, result.capture.Messages[0].ToolCalls[0].ID)
		assert.Equal(t, result.request, result.capture.Messages[1].ToolCallID)
		assert.Equal(t, "final "+result.request, result.capture.Messages[2].Content)
	}
}

func TestSessionCaptureSubAgentContextIsExplicitlyDisabled(t *testing.T) {
	turn := &TurnContext{Background: true, Config: &config.AgentConfig{}}
	var inheritedCapture *SessionCapture
	var inheritedTurn *TurnContext
	childModel := &sessionCaptureTestModel{stream: func(ctx context.Context, _ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		inheritedCapture = sessionCaptureFromContext(ctx)
		inheritedTurn = GetTurnContext(ctx)
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("private child answer", nil)}), nil
	}}
	child, err := adk.NewChatModelAgent(t.Context(), &adk.ChatModelAgentConfig{
		Name: "same-name", Description: "child", Model: childModel, MaxIterations: 4,
	})
	require.NoError(t, err)
	childTool := &sessionCaptureSubAgentTool{InvokableTool: adk.NewAgentTool(t.Context(), child).(tool.InvokableTool)}
	parentModel := &scriptedToolModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "child-call", Function: schema.FunctionCall{Name: "same-name", Arguments: `{"request":"private work"}`}}}}},
		{schema.AssistantMessage("parent answer", nil)},
	}}
	parent := newSessionCaptureAgent(t, parentModel, []tool.BaseTool{childTool}, 4)
	capture := NewSessionCapture()
	_, err = parent.Generate(WithSessionCapture(WithTurnContext(t.Context(), turn), capture), []*schema.Message{schema.UserMessage("parent request")})
	require.NoError(t, err)
	assert.Nil(t, inheritedCapture)
	assert.Same(t, turn, inheritedTurn)
	result := capture.Snapshot()
	require.NoError(t, result.Err)
	require.True(t, result.Complete)
	require.Len(t, result.Messages, 3)
	assert.Equal(t, "child-call", result.Messages[1].ToolCallID)
	assert.Equal(t, "private child answer", result.Messages[1].Content)
	assert.Equal(t, "parent answer", result.Messages[2].Content)
}

func TestSessionCaptureUnsupportedSnapshotsDoNotChangeOutput(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	for name, extra := range map[string]map[string]any{
		"function": {"callback": func() {}},
		"cycle":    cycle,
	} {
		t.Run(name, func(t *testing.T) {
			capture := NewSessionCapture()
			// This fake does not JSON-clone the unsupported input metadata.
			mdl := &sessionCaptureTestModel{stream: func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("answer", nil)}), nil
			}}
			agent := newSessionCaptureAgent(t, mdl, nil, 4)
			message, err := agent.Generate(WithSessionCapture(t.Context(), capture), []*schema.Message{{Role: schema.User, Content: "input", Extra: extra}})
			require.NoError(t, err)
			assert.Equal(t, "answer", message.Content)
			result := capture.Snapshot()
			require.Error(t, result.Err)
			assert.False(t, result.Complete)
		})
	}
}

func TestSessionCaptureCannotBeReused(t *testing.T) {
	capture := NewSessionCapture()
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("first", nil)}, {schema.AssistantMessage("second", nil)}}}
	agent := newSessionCaptureAgent(t, mdl, nil, 4)
	ctx := WithSessionCapture(t.Context(), capture)
	_, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage("first input")})
	require.NoError(t, err)
	require.True(t, capture.Snapshot().Complete)
	message, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage("second input")})
	require.NoError(t, err)
	assert.Equal(t, "second", message.Content)
	result := capture.Snapshot()
	require.ErrorIs(t, result.Err, errSessionCaptureReused)
	assert.False(t, result.Complete)
	assert.Equal(t, "first input", result.Input[0].Content)
	assert.Equal(t, "first", result.Messages[0].Content)
}

type sessionCaptureFailingTool struct{ lookupTool }

func (sessionCaptureFailingTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", errAgentFailureUnderTest
}

type sessionCaptureTestModel struct {
	stream func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error)
}

func (m *sessionCaptureTestModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.stream(ctx, input)
}

func (m *sessionCaptureTestModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	reader, err := m.stream(ctx, input)
	if err != nil {
		return nil, err
	}
	chunks, err := readSessionCaptureStream(reader)
	if err != nil {
		return nil, err
	}
	return schema.ConcatMessages(chunks)
}

func (m *sessionCaptureTestModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func newSessionCaptureAgent(t *testing.T, mdl model.ToolCallingChatModel, tools []tool.BaseTool, maxSteps int) *CustomAgent {
	t.Helper()
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "same-name", Model: mdl, Tools: tools, MaxSteps: maxSteps})
	require.NoError(t, err)
	return agent
}

func readSessionCaptureStream(reader *schema.StreamReader[*schema.Message]) ([]*schema.Message, error) {
	defer reader.Close()
	var chunks []*schema.Message
	for {
		chunk, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			return chunks, nil
		}
		if err != nil {
			return chunks, err
		}
		chunks = append(chunks, chunk)
	}
}
