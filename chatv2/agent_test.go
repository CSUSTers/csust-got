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
