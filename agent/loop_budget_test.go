package agentv3

import (
	"context"
	"sync"
	"testing"
	"time"

	"csust-got/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolAwareModel records, per model call, whether the call went through the tool-bound instance.
type toolAwareModel struct {
	script *scriptedToolModel
	bound  bool
	mu     *sync.Mutex
	calls  *[]bool
}

func newToolAwareModel(turns [][]*schema.Message) *toolAwareModel {
	return &toolAwareModel{script: &scriptedToolModel{turns: turns}, mu: &sync.Mutex{}, calls: &[]bool{}}
}

func (m *toolAwareModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.recordCall()
	return m.script.Generate(ctx, input, opts...)
}

func (m *toolAwareModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.recordCall()
	return m.script.Stream(ctx, input, opts...)
}

func (m *toolAwareModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &toolAwareModel{script: m.script, bound: true, mu: m.mu, calls: m.calls}, nil
}

func (m *toolAwareModel) recordCall() {
	m.mu.Lock()
	defer m.mu.Unlock()
	*m.calls = append(*m.calls, m.bound)
}

func (m *toolAwareModel) boundCalls() []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]bool(nil), (*m.calls)...)
}

type slowLookupTool struct{ delay time.Duration }

func (slowLookupTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return lookupTool{}.Info(ctx)
}

func (t slowLookupTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	select {
	case <-time.After(t.delay):
		return "slow result", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestLoopFinalRoundCallsModelWithoutTools(t *testing.T) {
	tests := []struct {
		name      string
		maxSteps  int
		turns     [][]*schema.Message
		wantBound []bool
		wantText  string
		wantTail  string
	}{
		{
			name:     "last budgeted call drops the tool binding",
			maxSteps: 2,
			turns: [][]*schema.Message{
				{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("call-1", `{}`)}}},
				{schema.AssistantMessage("final", nil)},
			},
			wantBound: []bool{true, false},
			wantText:  "final",
			wantTail:  finalTurnGuidance,
		},
		{
			name:     "tool calls on the final round trigger one forced summary",
			maxSteps: 1,
			turns: [][]*schema.Message{
				{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("call-1", `{}`)}}},
				{schema.AssistantMessage("summary", nil)},
			},
			wantBound: []bool{false, false},
			wantText:  "summary",
			wantTail:  forcedSummaryGuidance,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mdl := newToolAwareModel(tt.turns)
			counting := &countingLookupTool{}
			agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "final", Model: mdl, Tools: []tool.BaseTool{counting}, MaxSteps: tt.maxSteps})
			require.NoError(t, err)
			result, err := agent.Generate(t.Context(), []*schema.Message{schema.UserMessage("request")})
			require.NoError(t, err)
			assert.Equal(t, tt.wantText, result.Content)
			assert.Equal(t, tt.wantBound, mdl.boundCalls())
			inputs := mdl.script.capturedInputs()
			require.Len(t, inputs, len(tt.wantBound))
			last := inputs[len(inputs)-1]
			assert.Contains(t, last[len(last)-1].Content, tt.wantTail)
			for i := 1; i < len(inputs); i++ {
				assert.Equal(t, inputs[i-1], inputs[i][:len(inputs[i-1])], "each model call only appends to the previous input")
			}
		})
	}
}

func TestLoopForcedSummaryFallsBackToStepLimitNotice(t *testing.T) {
	call := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("call", `{}`)}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{call}, {call}}}
	counting := &countingLookupTool{}
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "fallback", Model: mdl, Tools: []tool.BaseTool{counting}, MaxSteps: 1})
	require.NoError(t, err)
	result, err := agent.Generate(t.Context(), []*schema.Message{schema.UserMessage("request")})
	require.NoError(t, err)
	assert.Contains(t, result.Content, "已达到本轮工具调用上限")
	assert.Zero(t, counting.callCount())
	assert.Len(t, mdl.capturedInputs(), 2)
}

func TestLoopEmptyModelResponseRetriesOnce(t *testing.T) {
	tests := []struct {
		name  string
		steps []retryStreamStep
		want  string
	}{
		{"retry recovers", []retryStreamStep{{}, {chunks: []*schema.Message{schema.AssistantMessage("recovered", nil)}}}, "recovered"},
		{"blank assistant then recovers", []retryStreamStep{{chunks: []*schema.Message{schema.AssistantMessage("", nil)}}, {chunks: []*schema.Message{schema.AssistantMessage("recovered", nil)}}}, "recovered"},
		{"still empty sends notice", []retryStreamStep{{}, {}}, emptyModelResponseNotice},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mdl := &retryStubModel{streamSteps: tt.steps}
			agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "empty", Model: mdl, MaxSteps: 4})
			require.NoError(t, err)
			result, err := agent.Generate(t.Context(), []*schema.Message{schema.UserMessage("request")})
			require.NoError(t, err)
			assert.Equal(t, tt.want, result.Content)
			assert.Equal(t, 2, mdl.streamCalls)
		})
	}
}

func TestLoopDeadlineReserveFinalizesWithoutTools(t *testing.T) {
	call := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("call", `{}`)}}
	mdl := newToolAwareModel([][]*schema.Message{{call}, {schema.AssistantMessage("wrapped up", nil)}})
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{
		Name:         "deadline",
		Model:        mdl,
		Tools:        []tool.BaseTool{slowLookupTool{delay: 1500 * time.Millisecond}},
		MaxSteps:     12,
		FinalReserve: 600 * time.Millisecond,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage("request")})
	require.NoError(t, err)
	assert.Equal(t, "wrapped up", result.Content)
	assert.Equal(t, []bool{true, false}, mdl.boundCalls(), "the second call starts inside the reserve and runs without tools")
	inputs := mdl.script.capturedInputs()
	require.Len(t, inputs, 2)
	last := inputs[1]
	assert.Contains(t, last[len(last)-1].Content, deadlineTurnGuidance)
	assert.Equal(t, inputs[0], last[:len(inputs[0])])
}

func TestEffectiveFinalReserveClampsToAThirdOfTheBudget(t *testing.T) {
	agent := &CustomAgent{finalReserve: 90 * time.Second}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	reserve := agent.effectiveFinalReserve(ctx)
	assert.LessOrEqual(t, reserve, 10*time.Second)
	assert.Greater(t, reserve, 9*time.Second)
	assert.Equal(t, 90*time.Second, agent.effectiveFinalReserve(t.Context()), "no deadline keeps the configured reserve")

	fromTurn := &CustomAgent{}
	turnCtx := WithTurnContext(t.Context(), &TurnContext{Config: &config.AgentConfig{Agent: &config.AgentOptions{FinalReserve: "20s"}}})
	assert.Equal(t, 20*time.Second, fromTurn.effectiveFinalReserve(turnCtx))
	assert.Equal(t, 90*time.Second, fromTurn.effectiveFinalReserve(t.Context()), "default reserve without turn config")
	assert.False(t, deadlineWithinReserve(t.Context(), time.Minute))
	assert.True(t, deadlineWithinReserve(ctx, time.Hour))
}

func TestBlankModelResponse(t *testing.T) {
	tests := []struct {
		name string
		msg  *schema.Message
		want bool
	}{
		{"nil", nil, true},
		{"empty assistant", schema.AssistantMessage("", nil), true},
		{"whitespace", schema.AssistantMessage("  \n", nil), true},
		{"metadata only", &schema.Message{Role: schema.Assistant, Extra: map[string]any{"k": true}}, true},
		{"text", schema.AssistantMessage("hi", nil), false},
		{"reasoning", &schema.Message{Role: schema.Assistant, ReasoningContent: "thinking"}, false},
		{"tool call", &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("c", "{}")}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, blankModelResponse(tt.msg))
		})
	}
}
