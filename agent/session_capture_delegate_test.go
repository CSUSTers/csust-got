package agentv3

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"csust-got/config"
	"csust-got/cronjob"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionCaptureDelegateInnerHistoryIsIsolated(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			turn := &TurnContext{Config: &config.AgentConfig{}}
			var inheritedCapture *SessionCapture
			var inheritedTurn *TurnContext
			childScript := &scriptedToolModel{turns: [][]*schema.Message{
				{{Role: schema.Assistant, ReasoningContent: "private inner reasoning", ToolCalls: []schema.ToolCall{lookupToolCall("private-inner-call", `{}`)}}},
				{schema.AssistantMessage("delegate result", nil)},
			}}
			childModel := &sessionCaptureTestModel{stream: func(ctx context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
				inheritedCapture = sessionCaptureFromContext(ctx)
				inheritedTurn = GetTurnContext(ctx)
				return childScript.Stream(ctx, input)
			}}
			counting := &countingLookupTool{}
			child := newSessionCaptureAgent(t, childModel, []tool.BaseTool{counting}, 4)
			delegate := &sessionCaptureDelegateTool{child: child}
			parentModel := &scriptedToolModel{turns: [][]*schema.Message{
				{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "delegate-call", Function: schema.FunctionCall{Name: agentV3ToolDelegate, Arguments: `{"request":"private inner request"}`}}}}},
				{schema.AssistantMessage("parent answer", nil)},
			}}
			parent := newSessionCaptureAgent(t, parentModel, []tool.BaseTool{delegate}, 4)
			capture := NewSessionCapture()
			ctx := WithSessionCapture(WithTurnContext(t.Context(), turn), capture)
			input := []*schema.Message{schema.UserMessage("parent request")}
			if streaming {
				reader, err := parent.Stream(ctx, input)
				require.NoError(t, err)
				_, err = readSessionCaptureStream(reader)
				require.NoError(t, err)
			} else {
				answer, err := parent.Generate(ctx, input)
				require.NoError(t, err)
				assert.Equal(t, "parent answer", answer.Content)
			}
			assert.Equal(t, 1, counting.callCount(), "the inner tool loop must actually run")
			assert.Len(t, childScript.capturedInputs(), 2)
			assert.Nil(t, delegate.inheritedCapture, "delegate entry must not inherit the parent recorder")
			assert.Nil(t, inheritedCapture, "inner model calls must not inherit the parent recorder")
			assert.Same(t, turn, inheritedTurn)
			result := capture.Snapshot()
			require.NoError(t, result.Err)
			require.True(t, result.Complete)
			assert.Equal(t, input, result.Input)
			require.Len(t, result.Messages, 3)
			require.Len(t, result.Messages[0].ToolCalls, 1)
			assert.Equal(t, "delegate-call", result.Messages[0].ToolCalls[0].ID)
			assert.Equal(t, agentV3ToolDelegate, result.Messages[0].ToolCalls[0].Function.Name)
			assert.Equal(t, schema.Tool, result.Messages[1].Role)
			assert.Equal(t, "delegate-call", result.Messages[1].ToolCallID)
			assert.Equal(t, agentV3ToolDelegate, result.Messages[1].ToolName)
			assert.Equal(t, "delegate result", result.Messages[1].Content)
			assert.Equal(t, "parent answer", result.Messages[2].Content)
			for _, message := range result.Messages {
				assert.NotContains(t, message.ReasoningContent, "private inner reasoning")
				assert.NotEqual(t, "private-inner-call", message.ToolCallID)
			}
		})
	}
}

func TestSessionCaptureBuiltinDelegateInvocationIsIsolated(t *testing.T) {
	f := newCronFixture(t)
	store := &sessionCaptureDelegateStore{Store: f.s.store}
	f.s.store = store
	args, err := json.Marshal(map[string]string{"cron": "@at 90s", "prompt": testCronPrompt})
	require.NoError(t, err)
	mdl := &scriptedToolModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "scheduled-delegate", Function: schema.FunctionCall{Name: agentV3ToolDelegate, Arguments: string(args)}}}}},
		{schema.AssistantMessage("task scheduled", nil)},
	}}
	tools := buildAgentV3Tools(f.tc.Config, config.BotConfig.AgentV3, agentV3SkillCatalog{}, nil)
	agent := newSessionCaptureAgent(t, mdl, tools, 4)
	capture := NewSessionCapture()
	answer, err := agent.Generate(WithSessionCapture(WithTurnContext(t.Context(), f.tc), capture), []*schema.Message{schema.UserMessage("schedule this task")})
	require.NoError(t, err)
	assert.Equal(t, "task scheduled", answer.Content)
	require.True(t, store.created, "the actual delegate tool must reach the Redis-backed cron store")
	assert.Nil(t, store.inheritedCapture)
	assert.Same(t, f.tc, store.inheritedTurn)
	result := capture.Snapshot()
	require.NoError(t, result.Err)
	require.True(t, result.Complete)
	require.Len(t, result.Messages, 3)
	assert.Equal(t, string(args), result.Messages[0].ToolCalls[0].Function.Arguments)
	assert.Equal(t, "scheduled-delegate", result.Messages[1].ToolCallID)
	assert.Equal(t, agentV3ToolDelegate, result.Messages[1].ToolName)
	var response struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Messages[1].Content), &response))
	require.NotEmpty(t, response.TaskID)
	task, err := store.Get(t.Context(), cronjob.GetRequest{Scope: cronjob.Scope{Bot: "cronbot", Platform: "tg", ChatID: f.tc.ChatID}, TaskID: response.TaskID})
	require.NoError(t, err)
	assert.Equal(t, testCronPrompt, task.Prompt)
	assert.Equal(t, f.tc.Config.Name, task.SourceAgent)
	assert.Nil(t, task.ActiveRun, "delegate creation must not immediately run the scheduled task")
	assert.Equal(t, "task scheduled", result.Messages[2].Content)
}

type sessionCaptureDelegateTool struct {
	child            *CustomAgent
	inheritedCapture *SessionCapture
}

func (t *sessionCaptureDelegateTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: agentV3ToolDelegate, Desc: "delegate to a private model loop"}, nil
}

func (t *sessionCaptureDelegateTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	t.inheritedCapture = sessionCaptureFromContext(ctx)
	var request struct {
		Request string `json:"request"`
	}
	if err := json.Unmarshal([]byte(args), &request); err != nil {
		return "", err
	}
	answer, err := t.child.Generate(ctx, []*schema.Message{schema.UserMessage(request.Request)})
	if err != nil {
		return "", err
	}
	return answer.Content, nil
}

type sessionCaptureDelegateStore struct {
	cronjob.Store
	created          bool
	inheritedCapture *SessionCapture
	inheritedTurn    *TurnContext
}

func (s *sessionCaptureDelegateStore) Create(ctx context.Context, request cronjob.CreateRequest) (cronjob.CreateResult, error) {
	s.created = true
	s.inheritedCapture = sessionCaptureFromContext(ctx)
	s.inheritedTurn = GetTurnContext(ctx)
	return s.Store.Create(ctx, request)
}
