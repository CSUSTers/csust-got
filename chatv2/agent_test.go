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
