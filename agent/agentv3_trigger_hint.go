package agentv3

import (
	"strings"

	"csust-got/config"
)

const (
	agentV3TriggerHintOpen  = "<trigger_hint>"
	agentV3TriggerHintClose = "</trigger_hint>"
)

// appendAgentV3TriggerHint appends the firing trigger's hint to the end of the current user text.
// The hint never enters the system prompt so the cached stable prefix is unaffected.
func appendAgentV3TriggerHint(userText string, trigger *config.AgentTrigger) string {
	if trigger == nil {
		return userText
	}
	hint := strings.TrimSpace(trigger.Hint)
	if hint == "" {
		return userText
	}
	block := agentV3TriggerHintOpen + hint + agentV3TriggerHintClose
	if strings.TrimSpace(userText) == "" {
		return block
	}
	return userText + "\n\n" + block
}
