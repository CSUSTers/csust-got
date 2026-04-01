//go:build !386 && !arm

package chatv2

import (
	"fmt"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
)

var errToolNodeBadFileID = fmt.Errorf("[NodeRunError] %w\n------------------------\nnode path: [tools]", errTestBadTelegramFile)

func TestShouldInjectFinalTurnGuidance(t *testing.T) {
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
		name     string
		maxSteps int
		messages []*schema.Message
		want     bool
	}{
		{
			name:     "no tools no final guidance",
			maxSteps: 4,
			messages: []*schema.Message{schema.UserMessage("hello")},
			want:     false,
		},
		{
			name:     "step budget 4 warns after one tool round",
			maxSteps: 4,
			messages: []*schema.Message{schema.UserMessage("search"), toolCallMsg},
			want:     true,
		},
		{
			name:     "step budget 5 still has room after one tool round",
			maxSteps: 5,
			messages: []*schema.Message{schema.UserMessage("search"), toolCallMsg},
			want:     false,
		},
		{
			name:     "step budget 5 warns after two tool rounds",
			maxSteps: 5,
			messages: []*schema.Message{schema.UserMessage("search"), toolCallMsg, toolCallMsg},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldInjectFinalTurnGuidance(tt.messages, tt.maxSteps))
		})
	}
}

func TestDetectToolCallLoop(t *testing.T) {
	makeToolCallMsg := func(name string) *schema.Message {
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "call_1", Function: schema.FunctionCall{Name: name, Arguments: "{}"}},
			},
		}
	}

	toolResult := &schema.Message{
		Role:    schema.Tool,
		Content: "some result",
	}

	tests := []struct {
		name     string
		messages []*schema.Message
		want     bool
	}{
		{
			name:     "no messages",
			messages: nil,
			want:     false,
		},
		{
			name: "only user message",
			messages: []*schema.Message{
				schema.UserMessage("hello"),
			},
			want: false,
		},
		{
			name: "two calls same tool no loop yet",
			messages: []*schema.Message{
				schema.UserMessage("search"),
				makeToolCallMsg("searxng_web_search"),
				toolResult,
				makeToolCallMsg("searxng_web_search"),
				toolResult,
			},
			want: false,
		},
		{
			name: "three consecutive calls same tool triggers loop detection",
			messages: []*schema.Message{
				schema.UserMessage("search"),
				makeToolCallMsg("searxng_web_search"),
				toolResult,
				makeToolCallMsg("searxng_web_search"),
				toolResult,
				makeToolCallMsg("searxng_web_search"),
				toolResult,
			},
			want: true,
		},
		{
			name: "update_progress calls are ignored in loop detection",
			messages: []*schema.Message{
				schema.UserMessage("search"),
				makeToolCallMsg("searxng_web_search"),
				toolResult,
				makeToolCallMsg("update_progress"),
				toolResult,
				makeToolCallMsg("searxng_web_search"),
				toolResult,
				makeToolCallMsg("update_progress"),
				toolResult,
				makeToolCallMsg("searxng_web_search"),
				toolResult,
			},
			want: true,
		},
		{
			name: "different tools interleaved no loop",
			messages: []*schema.Message{
				schema.UserMessage("search"),
				makeToolCallMsg("searxng_web_search"),
				toolResult,
				makeToolCallMsg("get_context"),
				toolResult,
				makeToolCallMsg("searxng_web_search"),
				toolResult,
			},
			want: false,
		},
		{
			name: "loop with different tool at start",
			messages: []*schema.Message{
				schema.UserMessage("search"),
				makeToolCallMsg("get_context"),
				toolResult,
				makeToolCallMsg("searxng_web_search"),
				toolResult,
				makeToolCallMsg("searxng_web_search"),
				toolResult,
				makeToolCallMsg("searxng_web_search"),
				toolResult,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, detectToolCallLoop(tt.messages))
		})
	}
}

func TestSelectGuidance(t *testing.T) {
	makeToolCallMsg := func(name string) *schema.Message {
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "call_1", Function: schema.FunctionCall{Name: name, Arguments: "{}"}},
			},
		}
	}

	toolResult := &schema.Message{
		Role:    schema.Tool,
		Content: "some result",
	}

	t.Run("no guidance when no tools called", func(t *testing.T) {
		msgs := []*schema.Message{schema.UserMessage("hello")}
		assert.Empty(t, selectGuidance(msgs, 12))
	})

	t.Run("loop detected returns loop guidance even with step budget remaining", func(t *testing.T) {
		msgs := []*schema.Message{
			schema.UserMessage("search"),
			makeToolCallMsg("web_search"),
			toolResult,
			makeToolCallMsg("web_search"),
			toolResult,
			makeToolCallMsg("web_search"),
			toolResult,
		}
		result := selectGuidance(msgs, 100) // large budget
		assert.Equal(t, loopDetectedGuidance, result)
	})

	t.Run("step budget exhausted returns final guidance", func(t *testing.T) {
		msgs := []*schema.Message{
			schema.UserMessage("search"),
			makeToolCallMsg("tool_a"),
			toolResult,
			makeToolCallMsg("tool_b"),
			toolResult,
		}
		result := selectGuidance(msgs, 5) // 2*2+3=7 > 5
		assert.Equal(t, finalTurnGuidance, result)
	})

	t.Run("loop guidance takes priority over final turn guidance", func(t *testing.T) {
		msgs := []*schema.Message{
			schema.UserMessage("search"),
			makeToolCallMsg("web_search"),
			toolResult,
			makeToolCallMsg("web_search"),
			toolResult,
			makeToolCallMsg("web_search"),
			toolResult,
		}
		result := selectGuidance(msgs, 5) // both would trigger
		assert.Equal(t, loopDetectedGuidance, result)
	})

	t.Run("zero max steps returns empty", func(t *testing.T) {
		assert.Empty(t, selectGuidance(nil, 0))
	})
}

func TestPrimaryToolCallName(t *testing.T) {
	t.Run("returns first tool name", func(t *testing.T) {
		msg := &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{Function: schema.FunctionCall{Name: "first_tool"}},
				{Function: schema.FunctionCall{Name: "second_tool"}},
			},
		}
		assert.Equal(t, "first_tool", primaryToolCallName(msg))
	})

	t.Run("empty tool calls returns empty", func(t *testing.T) {
		msg := &schema.Message{Role: schema.Assistant}
		assert.Empty(t, primaryToolCallName(msg))
	})
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
