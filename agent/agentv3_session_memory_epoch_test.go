package agentv3

import (
	"fmt"
	"testing"

	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestAgentV3SessionMemoryForgetRebuildsNewRoot(t *testing.T) {
	for _, forget := range []bool{false, true} {
		t.Run(fmt.Sprintf("forget=%t", forget), func(t *testing.T) {
			f := newAgentSessionFixture(t)
			cfg := &config.AgentConfig{Name: "epoch", ContextMode: "chat"}
			mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("ROOT_FINAL", nil)}, {schema.AssistantMessage("SECOND_FINAL", nil)}, {schema.AssistantMessage("THIRD_FINAL", nil)}}}
			f.compile(t, cfg, mdl)
			scope := orm.AgentV3Scope{Bot: f.scope().Bot, Platform: agentV3Platform, ChatID: -100}
			config.BotConfig.AgentV3.Memory.Enable = true
			require.NoError(t, addAgentV3Memory(t.Context(), scope, 7, "SECRET_MEMORY"))
			require.NoError(t, addAgentV3Memory(t.Context(), scope, 7, "KEPT_MEMORY"))

			first := f.chat(t, cfg, sessionMessage(10, 7, 0, "ROOT_INPUT"), nil)
			root := f.node(t, first)
			require.Zero(t, root.MemoryEpoch)
			require.Contains(t, replySessionSchemaText(sessionRecordMessages(f.archive(t, root).Bootstrap)), "SECRET_MEMORY")
			if forget {
				items, err := orm.AgentV3ListMemory(t.Context(), scope)
				require.NoError(t, err)
				for _, item := range items {
					if item.Content == "SECRET_MEMORY" {
						require.NoError(t, orm.AgentV3ForgetMemory(t.Context(), scope, item.ID))
					}
				}
				require.NoError(t, rebuildAgentV3MemorySnapshot(t.Context(), scope, agentV3MemoryTTL()))
			}

			reply := sessionMessage(20, 8, 60, "SECOND_INPUT")
			reply.ReplyTo = &tb.Message{ID: first}
			second := f.chat(t, cfg, reply, &config.AgentTrigger{Reply: true})
			node := f.node(t, second)
			text := replySessionSchemaText(mdl.capturedInputs()[1])
			require.Contains(t, text, "KEPT_MEMORY")
			if forget {
				require.Nil(t, node.Parent, "memory deleted since the node: the reply rebuilds a new root")
				require.NotEqual(t, root.Ref.DAGID, node.Ref.DAGID)
				require.Equal(t, int64(2), node.MemoryEpoch)
				require.NotContains(t, text, "SECRET_MEMORY", "the deleted memory is not replayed to the provider")
				require.Contains(t, text, "ROOT_INPUT", "the legacy context still carries the earlier turn")
				capture := f.archive(t, node)
				require.NotContains(t, replySessionSchemaText(append(sessionRecordMessages(capture.Bootstrap), sessionRecordMessages(capture.Delta)...)), "SECRET_MEMORY")
			} else {
				require.Equal(t, &root.Ref, node.Parent, "without a deletion the reply continues the node")
				require.Zero(t, node.MemoryEpoch)
				require.Contains(t, text, "SECRET_MEMORY", "the archived snapshot is replayed verbatim")
			}

			next := sessionMessage(30, 7, 120, "THIRD_INPUT")
			next.ReplyTo = &tb.Message{ID: second}
			third := f.chat(t, cfg, next, &config.AgentTrigger{Reply: true})
			require.Equal(t, &node.Ref, f.node(t, third).Parent, "the current-epoch node continues normally")
			if forget {
				require.NotContains(t, replySessionSchemaText(mdl.capturedInputs()[2]), "SECRET_MEMORY")
			}
		})
	}
}
