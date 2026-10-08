package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentTriggerHintValidation(t *testing.T) {
	tests := []struct {
		name    string
		hint    string
		wantErr bool
	}{
		{name: "empty", hint: ""},
		{name: "at limit", hint: strings.Repeat("好", AgentTriggerHintMaxChars)},
		{name: "over limit", hint: strings.Repeat("好", AgentTriggerHintMaxChars+1), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trigger := &AgentTrigger{Command: "sear", Hint: tt.hint}
			err := trigger.Validate()
			agentErr := (&AgentConfig{Name: "assistant", Trigger: []*AgentTrigger{{Command: "chat"}, trigger}}).ValidateTriggers()
			if tt.wantErr {
				require.ErrorIs(t, err, errAgentTriggerHintTooLong)
				require.ErrorIs(t, agentErr, errAgentTriggerHintTooLong)
				require.Contains(t, agentErr.Error(), `agent "assistant" trigger[1]`)
				return
			}
			require.NoError(t, err)
			require.NoError(t, agentErr)
		})
	}
	var nilTrigger *AgentTrigger
	require.NoError(t, nilTrigger.Validate())
	require.ErrorIs(t, (*AgentConfig)(nil).ValidateTriggers(), errAgentConfigNil)
}

func TestSubAgentConfigRuntimeDefaults(t *testing.T) {
	tests := []struct {
		name            string
		cfg             *SubAgentConfig
		wantSteps       int
		wantResultChars int
	}{
		{name: "nil", cfg: nil, wantSteps: defaultSubAgentMaxSteps, wantResultChars: defaultSubAgentMaxResultChars},
		{name: "plain", cfg: &SubAgentConfig{}, wantSteps: defaultSubAgentMaxSteps, wantResultChars: defaultSubAgentMaxResultChars},
		{name: "runtime default", cfg: &SubAgentConfig{Runtime: true}, wantSteps: defaultRuntimeSubAgentMaxSteps, wantResultChars: defaultSubAgentMaxResultChars},
		{name: "runtime too low is clamped", cfg: &SubAgentConfig{Runtime: true, MaxSteps: 2}, wantSteps: minToolAgentMaxSteps, wantResultChars: defaultSubAgentMaxResultChars},
		{name: "skills only count as tools", cfg: &SubAgentConfig{Skills: []string{"searxng"}, MaxSteps: 1}, wantSteps: minToolAgentMaxSteps, wantResultChars: defaultSubAgentMaxResultChars},
		{name: "explicit values", cfg: &SubAgentConfig{Runtime: true, MaxSteps: 12, MaxResultChars: 900}, wantSteps: 12, wantResultChars: 900},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantSteps, tt.cfg.GetMaxSteps())
			require.Equal(t, tt.wantResultChars, tt.cfg.GetMaxResultChars())
		})
	}
}

func TestSubAgentConfigValidateSkills(t *testing.T) {
	require.NoError(t, (*SubAgentConfig)(nil).ValidateSkills())
	require.NoError(t, (&SubAgentConfig{Name: "r", Skills: []string{"searxng", "Web_Search", " rich-message "}}).ValidateSkills())
	err := (&SubAgentConfig{Name: "r", Skills: []string{"../disk"}}).ValidateSkills()
	require.ErrorIs(t, err, errInvalidSubAgentSkillName)
	agent := &AgentConfig{Name: "assistant", Agent: &AgentOptions{SubAgents: []*SubAgentConfig{{Name: "r", Skills: []string{"bad name"}}}}}
	require.ErrorIs(t, agent.ValidateSubAgents(), errInvalidSubAgentSkillName)
	require.NoError(t, (&AgentConfig{Name: "plain"}).ValidateSubAgents())
}

func TestAgentV3MemoryWritePolicyQuota(t *testing.T) {
	cfg := &AgentV3Config{Memory: AgentV3MemoryConfig{WritePolicy: "explicit_quota"}}
	cfg.checkConfig()
	require.Equal(t, "explicit_quota", cfg.Memory.WritePolicy)
	require.True(t, cfg.Memory.QuotaWrites())
	require.Equal(t, 20, cfg.Memory.MaxEntriesPerUser)
	require.Equal(t, 20, cfg.Memory.EffectiveMaxEntriesPerUser())

	custom := AgentV3MemoryConfig{WritePolicy: "explicit_or_admin", MaxEntriesPerUser: 3}
	require.False(t, custom.QuotaWrites())
	require.Equal(t, 3, custom.EffectiveMaxEntriesPerUser())
	require.Equal(t, 20, AgentV3MemoryConfig{MaxEntriesPerUser: -1}.EffectiveMaxEntriesPerUser())
}
