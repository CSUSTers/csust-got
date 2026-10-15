package agentv3

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"csust-got/agent/session"
	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestAgentV3SessionUsesAgentLimitIncludingActualReply(t *testing.T) {
	for _, reply := range []bool{false, true} {
		for _, agentLimit := range []int64{1, 200000} {
			t.Run(fmt.Sprintf("reply=%t/limit=%d", reply, agentLimit), func(t *testing.T) {
				f := newAgentSessionFixture(t)
				save := false
				cfg := &config.AgentConfig{Name: "agent-limit", ContextMode: "reply_chain", Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: true, ContextOverflow: config.AgentSessionContextOverflowConfig{MaxTokens: &agentLimit}}}
				mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}}, before: func(ctx context.Context, _ []*schema.Message) error {
					if agentLimit == 1 {
						require.Nil(t, GetTurnContext(ctx).Session.parent)
					} else {
						require.NotNil(t, GetTurnContext(ctx).Session.parent)
					}
					return nil
				}}
				f.compile(t, cfg, mdl)
				cfg.Model.PromptLimit = 1
				globalLimit := int64(1)
				if agentLimit == 1 {
					globalLimit = 200000
				}
				config.BotConfig.AgentV3.Session.ContextOverflow.MaxTokens = globalLimit
				seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage(strings.Repeat("old", 100)), schema.AssistantMessage("answer", nil)}, nil)
				repo := installAgentSessionGate(t, f, session.Options{})
				current := sessionMessage(1100, 7, 0, "current")
				current.ReplyTo = sessionMessage(50, 8, 0, "quoted")
				var trigger *config.AgentTrigger
				if reply {
					trigger = &config.AgentTrigger{Reply: true}
				}
				f.chat(t, cfg, current, trigger)
				wantConfirms := int32(0)
				if agentLimit != 1 {
					wantConfirms = 1
				}
				require.Equal(t, wantConfirms, repo.confirms.Load())
				require.Len(t, mdl.capturedInputs(), 1)
			})
		}
	}
}
