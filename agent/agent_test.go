package agentv3

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errToolNodeBadFileID = fmt.Errorf("[NodeRunError] %w\n------------------------\nnode path: [tools]", errTestBadTelegramFile)

var errAgentFailureUnderTest = errors.New("agent failure")

func TestCalcGuidanceLevel(t *testing.T) {
	toolCallMsg := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{
				ID: "call_1",
				Function: schema.FunctionCall{
					Name:      "searxng_web_search",
					Arguments: "{}",
				},
			},
		},
	}

	tests := []struct {
		name       string
		maxSteps   int
		messages   []*schema.Message
		wantLevel  guidanceLevel
		wantRounds int
	}{
		{
			name:       "no tools no guidance",
			maxSteps:   4,
			messages:   []*schema.Message{schema.UserMessage("hello")},
			wantLevel:  guidanceNone,
			wantRounds: 0,
		},
		{
			name:       "step budget 4 hard stop after one tool round",
			maxSteps:   4,
			messages:   []*schema.Message{schema.UserMessage("search"), toolCallMsg},
			wantLevel:  guidanceHard,
			wantRounds: 1,
		},
		{
			name:       "step budget 12 no guidance after one tool round",
			maxSteps:   12,
			messages:   []*schema.Message{schema.UserMessage("search"), toolCallMsg},
			wantLevel:  guidanceNone,
			wantRounds: 1,
		},
		{
			name:       "step budget 12 soft nudge after two tool rounds",
			maxSteps:   12,
			messages:   []*schema.Message{schema.UserMessage("search"), toolCallMsg, toolCallMsg},
			wantLevel:  guidanceSoft,
			wantRounds: 2,
		},
		{
			name:       "step budget 12 soft nudge after three tool rounds",
			maxSteps:   12,
			messages:   []*schema.Message{schema.UserMessage("search"), toolCallMsg, toolCallMsg, toolCallMsg},
			wantLevel:  guidanceSoft,
			wantRounds: 3,
		},
		{
			name:       "step budget 12 hard stop after four tool rounds",
			maxSteps:   12,
			messages:   []*schema.Message{schema.UserMessage("search"), toolCallMsg, toolCallMsg, toolCallMsg, toolCallMsg},
			wantLevel:  guidanceHard,
			wantRounds: 4,
		},
		{
			name:       "maxSteps 0 always returns none",
			maxSteps:   0,
			messages:   []*schema.Message{schema.UserMessage("search"), toolCallMsg, toolCallMsg},
			wantLevel:  guidanceNone,
			wantRounds: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, rounds := calcGuidanceLevel(tt.messages, tt.maxSteps)
			assert.Equal(t, tt.wantLevel, level)
			assert.Equal(t, tt.wantRounds, rounds)
		})
	}
}

func TestFriendlyAgentErrorMessage(t *testing.T) {
	t.Run("max step errors become user friendly", func(t *testing.T) {
		msg := friendlyAgentErrorMessage(fmt.Errorf("[GraphRunError] %w", compose.ErrExceedMaxSteps))
		assert.Contains(t, msg, "步骤上限")
	})

	t.Run("known image tool errors keep the allowlisted message", func(t *testing.T) {
		msg := friendlyAgentErrorMessage(errToolNodeBadFileID)
		assert.Contains(t, msg, "Telegram 图片不可用")
	})

	t.Run("tool errors do not expose internal details", func(t *testing.T) {
		secret := "Bearer secret-token"
		internalURL := "http://redis.internal:6379/runtime"
		windowsPath := `C:\\agent\\secrets\\config.yaml`
		unixPath := "/var/lib/redis/dump.rdb"
		err := fmt.Errorf("%w: [NodeRunError] tool failed: %s %s %s %s\n------------------------\nnode path: [tools]\nstack detail", errAgentFailureUnderTest, secret, internalURL, windowsPath, unixPath)

		msg := friendlyAgentErrorMessage(err)

		assert.Contains(t, msg, "工具调用阶段")
		for _, sensitive := range []string{secret, internalURL, windowsPath, unixPath, "stack detail"} {
			assert.NotContains(t, msg, sensitive)
		}
	})

	t.Run("answer errors do not expose internal details", func(t *testing.T) {
		secret := "Bearer secret-token"
		err := fmt.Errorf("%w: [GraphRunError] generation failed: %s\n------------------------\nstack detail", errAgentFailureUnderTest, secret)

		msg := friendlyAgentErrorMessage(err)

		assert.Contains(t, msg, "回答生成阶段")
		assert.NotContains(t, msg, secret)
		assert.NotContains(t, msg, "stack detail")
	})
}

func TestGenerateDropsIntermediateToolTurnOutput(t *testing.T) {
	ctx := t.Context()
	mdl := &scriptedToolModel{
		turns: [][]*schema.Message{
			{
				{Role: schema.Assistant, Content: "我先查一下。"},
				{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{
						{
							ID: "call_1",
							Function: schema.FunctionCall{
								Name:      "lookup",
								Arguments: `{"q":"x"}`,
							},
						},
					},
				},
			},
			{schema.AssistantMessage("最终答案", nil)},
		},
	}
	agent, err := NewCustomAgent(ctx, &CustomAgentConfig{
		Name:     "test",
		Model:    mdl,
		Tools:    []tool.BaseTool{lookupTool{}},
		MaxSteps: 4,
	})
	require.NoError(t, err)

	msg, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage("问题")})
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "最终答案", msg.Content)
}

func TestStreamOneTurnForwardsClearOutputAndDropsPartialBeforeRetry(t *testing.T) {
	ctx := t.Context()
	mdl := &scriptedToolModel{
		turns: [][]*schema.Message{
			{
				schema.AssistantMessage("partial", nil),
				newClearStreamOutputMessage(),
				schema.AssistantMessage("final", nil),
			},
		},
	}
	agent, err := NewCustomAgent(ctx, &CustomAgentConfig{
		Name:     "test",
		Model:    mdl,
		Tools:    []tool.BaseTool{lookupTool{}},
		MaxSteps: 4,
	})
	require.NoError(t, err)
	sr, sw := schema.Pipe[*schema.Message](8)

	msg, _, err := agent.streamOneTurn(ctx, agent.boundModel, []*schema.Message{schema.UserMessage("问题")}, sw)
	sw.Close()

	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "final", msg.Content)

	first, recvErr := sr.Recv()
	require.NoError(t, recvErr)
	assert.Equal(t, "partial", first.Content)
	clearMsg, recvErr := sr.Recv()
	require.NoError(t, recvErr)
	assert.True(t, isClearStreamOutputMessage(clearMsg))
	third, recvErr := sr.Recv()
	require.NoError(t, recvErr)
	assert.Equal(t, "final", third.Content)
}

type scriptedToolModel struct {
	mu    sync.Mutex
	turns [][]*schema.Message
	next  int
}

func (m *scriptedToolModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	stream, err := m.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	var chunks []*schema.Message
	for {
		chunk, recvErr := stream.Recv()
		if recvErr != nil {
			break
		}
		chunks = append(chunks, chunk)
	}
	return schema.ConcatMessages(chunks)
}

func (m *scriptedToolModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.next >= len(m.turns) {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", nil)}), nil
	}
	turn := m.turns[m.next]
	m.next++
	return schema.StreamReaderFromArray(turn), nil
}

func (m *scriptedToolModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type lookupTool struct{}

func (lookupTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "lookup",
		Desc: "lookup test data",
	}, nil
}

func (lookupTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "tool result", nil
}
