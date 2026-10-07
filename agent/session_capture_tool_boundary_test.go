package agentv3

import (
	"context"
	"testing"
	"time"

	"csust-got/agent/session"
	"csust-got/config"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

type sessionBoundaryContextKey struct{}

type sessionBoundaryNestedTool struct {
	name  string
	child *CustomAgent
	check func(context.Context)
}

func (t *sessionBoundaryNestedTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "ordinary tool with an internal model loop"}, nil
}

func (t *sessionBoundaryNestedTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	t.check(ctx)
	answer, err := t.child.Generate(ctx, []*schema.Message{schema.UserMessage("private child input")})
	if err != nil {
		return "", err
	}
	return answer.Content, nil
}

func TestSessionCaptureOrdinaryNestedToolBoundaryArchiveCommitLoad(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "generate", true: "stream"}[streaming], func(t *testing.T) {
			f := newAgentSessionFixture(t)
			turn := &TurnContext{Config: &config.AgentConfig{}, V3: &AgentV3TurnState{}}
			ctx, cancel := context.WithTimeout(WithTurnContext(t.Context(), turn), time.Minute)
			defer cancel()
			ctx = context.WithValue(ctx, sessionBoundaryContextKey{}, turn)
			deadline, _ := ctx.Deadline()
			checks := 0
			check := func(childCtx context.Context) {
				checks++
				require.Nil(t, sessionCaptureFromContext(childCtx))
				require.Same(t, turn, GetTurnContext(childCtx), "runtime/permissions/trace stay on the same turn")
				require.Same(t, turn, childCtx.Value(sessionBoundaryContextKey{}))
				got, ok := childCtx.Deadline()
				require.True(t, ok)
				require.Equal(t, deadline, got)
			}
			childScript := &scriptedToolModel{turns: [][]*schema.Message{
				{{Role: schema.Assistant, ReasoningContent: "private child reasoning", ToolCalls: []schema.ToolCall{lookupToolCall("private-call", "{}")}}},
				{schema.AssistantMessage("public tool result", nil)},
			}}
			child := newSessionCaptureAgent(t, &sessionCaptureTestModel{stream: func(childCtx context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				check(childCtx)
				return childScript.Stream(childCtx, input)
			}}, []tool.BaseTool{lookupTool{}}, 4)
			ordinary := &sessionBoundaryNestedTool{name: "ordinary_not_delegate", child: child, check: check}
			mdl := &scriptedToolModel{turns: [][]*schema.Message{
				{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "outer-call", Function: schema.FunctionCall{Name: ordinary.name, Arguments: "{}"}}}}},
				{schema.AssistantMessage("top complete", nil)},
			}}
			parent := newSessionCaptureAgent(t, mdl, []tool.BaseTool{ordinary}, 4)
			capture := NewSessionCapture()
			input := []*schema.Message{schema.UserMessage("parent input")}
			ctx = WithSessionCapture(ctx, capture)
			if streaming {
				reader, err := parent.Stream(ctx, input)
				require.NoError(t, err)
				_, err = readSessionCaptureStream(reader)
				require.NoError(t, err)
			} else {
				answer, err := parent.Generate(ctx, input)
				require.NoError(t, err)
				require.Equal(t, "top complete", answer.Content)
			}
			<-capture.Done()
			require.Equal(t, 3, checks)
			result := capture.Snapshot()
			require.NoError(t, result.Err)
			require.True(t, result.Complete)
			require.Len(t, result.Messages, 3)
			require.Equal(t, "outer-call", result.Messages[0].ToolCalls[0].ID)
			require.Equal(t, "outer-call", result.Messages[1].ToolCallID)
			archive, err := agentV3SessionArchive(&agentV3SessionTurn{input: input, kinds: []agentV3SessionInputKind{agentV3SessionCurrent}}, result)
			require.NoError(t, err)
			runID, err := session.NewID()
			require.NoError(t, err)
			_, err = f.service.Commit(t.Context(), session.CommitRequest{Scope: f.scope(), Agent: "ordinary", RunID: runID, Capture: archive, Receipt: session.DeliveryReceipt{MessageIDs: []int{77}}})
			require.NoError(t, err)
			loaded, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: 77})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, loaded.Parent.Close()) })
			require.Len(t, loaded.Messages, 4)
			for _, message := range loaded.Messages {
				require.NotContains(t, message.Content, "private child input")
				require.NotContains(t, message.ReasoningContent, "private child reasoning")
				require.NotEqual(t, "private-call", message.ToolCallID)
			}
		})
	}
}

func TestSessionCaptureADKWrapperStandaloneBoundary(t *testing.T) {
	turn := &TurnContext{}
	mdl := &sessionCaptureTestModel{stream: func(ctx context.Context, _ []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		require.Nil(t, sessionCaptureFromContext(ctx))
		require.Same(t, turn, GetTurnContext(ctx))
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("child", nil)}), nil
	}}
	child, err := adk.NewChatModelAgent(t.Context(), &adk.ChatModelAgentConfig{Name: "standalone", Model: mdl, MaxIterations: 2})
	require.NoError(t, err)
	wrapper := &sessionCaptureSubAgentTool{InvokableTool: adk.NewAgentTool(t.Context(), child).(tool.InvokableTool)}
	capture := NewSessionCapture()
	_, err = wrapper.InvokableRun(WithSessionCapture(WithTurnContext(t.Context(), turn), capture), `{"request":"child request"}`)
	require.NoError(t, err)
	require.False(t, capture.started)
	require.Empty(t, capture.Snapshot().Messages)
}

func TestSessionCaptureModelMutationLeavesInvocationAndBaselineDetached(t *testing.T) {
	input := []*schema.Message{{Role: schema.User, Content: "original", Extra: map[string]any{"values": []int{1, 2}}}}
	mdl := &sessionCaptureTestModel{stream: func(_ context.Context, messages []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		messages[0].Content = "model mutation"
		messages[0].Extra["values"].([]int)[0] = 99
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("answer", nil)}), nil
	}}
	a := newSessionCaptureAgent(t, mdl, nil, 2)
	capture := NewSessionCapture()
	_, err := a.Generate(WithSessionCapture(t.Context(), capture), input)
	require.NoError(t, err)
	result := capture.Snapshot()
	require.NoError(t, result.Err)
	require.True(t, result.Complete)
	require.Equal(t, input, result.Input)
	require.Equal(t, input, result.ModelInput)
	require.Equal(t, "original", input[0].Content)
	require.Equal(t, []int{1, 2}, input[0].Extra["values"])
}
