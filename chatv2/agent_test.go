//go:build !386 && !arm

package chatv2

import (
	"context"
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

	t.Run("tool node errors mention tool stage", func(t *testing.T) {
		msg := friendlyAgentErrorMessage(errToolNodeBadFileID)
		assert.Contains(t, msg, "工具调用阶段")
		assert.Contains(t, msg, "Telegram 图片不可用")
	})
}

func TestGenerateDropsIntermediateToolTurnOutput(t *testing.T) {
	ctx := context.Background()
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
