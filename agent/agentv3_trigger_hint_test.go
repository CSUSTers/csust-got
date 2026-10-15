package agentv3

import (
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/require"
)

func TestAppendAgentV3TriggerHint(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		trigger *config.AgentTrigger
		want    string
	}{
		{name: "nil trigger", text: "hello", trigger: nil, want: "hello"},
		{name: "empty hint", text: "hello", trigger: &config.AgentTrigger{Command: "chat"}, want: "hello"},
		{name: "blank hint", text: "hello", trigger: &config.AgentTrigger{Command: "chat", Hint: "  \n"}, want: "hello"},
		{name: "appended at end", text: "<USER_INPUT>\nhello\n</USER_INPUT>", trigger: &config.AgentTrigger{Command: "sear", Hint: " search first "}, want: "<USER_INPUT>\nhello\n</USER_INPUT>\n\n<trigger_hint>search first</trigger_hint>"},
		{name: "empty text", text: "", trigger: &config.AgentTrigger{Reply: true, Hint: "be brief"}, want: "<trigger_hint>be brief</trigger_hint>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, appendAgentV3TriggerHint(tt.text, tt.trigger))
		})
	}
}
