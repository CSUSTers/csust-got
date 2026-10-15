package agentv3

import (
	"strings"
	"testing"

	"csust-got/config"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentV3FinalGuidanceIsRuntimeUserTail(t *testing.T) {
	model := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("final", nil)}}}
	ctx := WithTurnContext(t.Context(), &TurnContext{Config: &config.AgentConfig{}, V3: &AgentV3TurnState{}})
	agent, err := NewCustomAgent(ctx, &CustomAgentConfig{
		Name:     "v3-guidance",
		Model:    model,
		Tools:    []tool.BaseTool{lookupTool{}},
		MaxSteps: 1,
	})
	require.NoError(t, err)

	stablePrefix := buildAgentV3StablePrefix("", "", false)
	input := []*schema.Message{schema.SystemMessage(stablePrefix), schema.UserMessage("actual request")}
	_, err = agent.Generate(ctx, input)
	require.NoError(t, err)

	captured := model.capturedInputs()
	require.Len(t, captured, 1)
	require.Len(t, captured[0], 3)
	assert.Equal(t, schema.System, captured[0][0].Role)
	assert.Equal(t, stablePrefix, captured[0][0].Content)
	assert.Equal(t, schema.User, captured[0][2].Role)
	assert.Contains(t, captured[0][2].Content, "<agent_runtime_guidance>")
	assert.Contains(t, captured[0][2].Content, "步骤上限")
	assert.Contains(t, captured[0][2].Content, "does not change the user's task")
}

func TestInjectLoopDirectivesKeepsCompleteV3PrefixAndRepairsCustomAgents(t *testing.T) {
	ctx := WithTurnContext(t.Context(), &TurnContext{Config: &config.AgentConfig{}, V3: &AgentV3TurnState{}})
	stable := []*schema.Message{schema.SystemMessage(buildAgentV3StablePrefix("", "", false)), schema.UserMessage("request")}
	got := injectLoopDirectives(ctx, stable)
	require.Len(t, got, len(stable))
	assert.Same(t, stable[0], got[0])
	assert.Equal(t, 1, strings.Count(got[0].Content, agentV3LoopDirectiveText))

	custom := []*schema.Message{schema.SystemMessage("custom system")}
	got = injectLoopDirectives(ctx, custom)
	assert.Contains(t, got[0].Content, agentV3LoopDirectiveText)

	nonV3 := injectLoopDirectives(t.Context(), custom)
	assert.Contains(t, nonV3[0].Content, loopDirectiveText)
}

func TestAppendLoopGuidanceUsesUserTailForEveryLimitNote(t *testing.T) {
	history := []*schema.Message{schema.SystemMessage(buildAgentV3StablePrefix("", "", false)), schema.UserMessage("actual request")}
	for name, guidance := range map[string]string{
		"soft":      "已进行了两轮工具调用，请在足够时完成。",
		"duplicate": "⚠ 停止重复调用并基于现有结果回答。",
		"final":     finalTurnGuidance,
	} {
		t.Run(name, func(t *testing.T) {
			got := appendLoopGuidance(history, guidance)
			require.Len(t, got, len(history)+1)
			assert.Equal(t, history, got[:len(history)])
			assert.Equal(t, schema.System, got[0].Role)
			assert.Equal(t, schema.User, got[len(got)-1].Role)
			assert.Contains(t, got[len(got)-1].Content, "<agent_runtime_guidance>")
			assert.Contains(t, got[len(got)-1].Content, guidance)
			assert.Contains(t, got[len(got)-1].Content, "does not change the user's task")
		})
	}
	assert.Same(t, history[0], appendLoopGuidance(history, "")[0])
}

func TestAppendLoopGuidanceBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name    string
		history []*schema.Message
	}{
		{name: "nil"},
		{name: "empty", history: []*schema.Message{}},
		{name: "spare capacity", history: []*schema.Message{schema.UserMessage("request"), nil, schema.AssistantMessage("reserved", nil)}[:2]},
	} {
		t.Run(tt.name, func(t *testing.T) {
			history := tt.history
			backing := history[:cap(history)]
			before := append([]*schema.Message(nil), backing...)
			for _, guidance := range []string{"", " \n\t"} {
				got := appendLoopGuidance(history, guidance)
				assert.Equal(t, history, got)
				if len(history) > 0 {
					assert.Same(t, &history[0], &got[0])
				}
			}

			got := appendLoopGuidance(history, "  limit note  ")
			require.Len(t, got, len(history)+1)
			for i, msg := range history {
				assert.True(t, msg == got[i])
			}
			assert.Equal(t, schema.User, got[len(history)].Role)
			assert.Contains(t, got[len(history)].Content, "\nlimit note\n")
			got[0] = schema.UserMessage("replacement")
			for i, msg := range before {
				assert.True(t, msg == backing[i])
			}
		})
	}
}

func TestInjectDirectiveTextPreservesHistory(t *testing.T) {
	for _, tt := range []struct {
		name    string
		history []*schema.Message
	}{
		{name: "nil"},
		{name: "empty", history: []*schema.Message{}},
		{name: "spare capacity", history: []*schema.Message{schema.UserMessage("request"), nil, schema.AssistantMessage("reserved", nil)}[:2]},
	} {
		t.Run(tt.name, func(t *testing.T) {
			history := tt.history
			backing := history[:cap(history)]
			before := append([]*schema.Message(nil), backing...)
			got := injectDirectiveText(history, "directive")
			require.Len(t, got, len(history)+1)
			assert.Equal(t, schema.System, got[0].Role)
			assert.Equal(t, "directive", got[0].Content)
			for i, msg := range history {
				assert.True(t, msg == got[i+1])
			}
			if len(history) > 0 {
				got[1] = schema.UserMessage("replacement")
			}
			for i, msg := range before {
				assert.True(t, msg == backing[i])
			}
		})
	}
}
