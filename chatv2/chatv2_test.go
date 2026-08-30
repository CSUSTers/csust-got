//go:build !386 && !arm

package chatv2

import (
	"context"
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitRejectsInvalidAgentV3RuntimeBeforeCompilation(t *testing.T) {
	oldConfig := config.BotConfig
	oldMcpManager := mcpManager
	oldCompiledChats := snapshotCompiledChats()
	t.Cleanup(func() {
		config.BotConfig = oldConfig
		mcpManager = oldMcpManager
		restoreCompiledChats(oldCompiledChats)
	})

	config.BotConfig = &config.Config{
		Agents: &config.ChatConfigV2{
			{Name: "invalid-agent-v3", Agent: &config.AgentConfig{Enable: true, V3: true}},
		},
		AgentV3: &config.AgentV3Config{Enable: true},
	}
	mcpManager = nil
	clearCompiledChats()
	compiledChats.Store("existing", &CompiledChat{})

	err := Init(context.Background())

	require.Error(t, err)
	assert.ErrorContains(t, err, `agent v3 chat "invalid-agent-v3" cannot use runtime`)
	assert.ErrorContains(t, err, "runtime is disabled")
	assert.ErrorIs(t, err, errAgentV3RuntimeDisabled)
	assert.Nil(t, mcpManager)
	assert.True(t, HasCompiledChat("existing"))
	assert.False(t, HasCompiledChat("invalid-agent-v3"))
}

func TestValidateAgentV3StartupConfig(t *testing.T) {
	oldConfig := config.BotConfig
	t.Cleanup(func() { config.BotConfig = oldConfig })

	agentV3Chat := &config.ChatConfigSingle{
		Name:  "agent-v3",
		Agent: &config.AgentConfig{Enable: true, V3: true},
	}
	nonV3Chat := &config.ChatConfigSingle{
		Name:  "agent-v2",
		Agent: &config.AgentConfig{Enable: true},
	}

	tests := []struct {
		name         string
		botConfig    *config.Config
		wantErr      bool
		wantContains string
	}{
		{
			name:      "nil bot config",
			botConfig: nil,
		},
		{
			name: "nil agent v3 config",
			botConfig: &config.Config{
				Agents: &config.ChatConfigV2{agentV3Chat},
			},
		},
		{
			name: "nil agents",
			botConfig: &config.Config{
				AgentV3: &config.AgentV3Config{Enable: true},
			},
		},
		{
			name: "agent v3 globally disabled",
			botConfig: &config.Config{
				Agents:  &config.ChatConfigV2{agentV3Chat},
				AgentV3: &config.AgentV3Config{},
			},
		},
		{
			name: "no enabled agent v3 chats",
			botConfig: &config.Config{
				Agents:  &config.ChatConfigV2{nonV3Chat},
				AgentV3: &config.AgentV3Config{Enable: true},
			},
		},
		{
			name: "disabled runtime",
			botConfig: &config.Config{
				Agents:  &config.ChatConfigV2{agentV3Chat},
				AgentV3: &config.AgentV3Config{Enable: true},
			},
			wantErr:      true,
			wantContains: "runtime is disabled",
		},
		{
			name: "valid remote HTTP runtime and system prompt skills",
			botConfig: &config.Config{
				Agents: &config.ChatConfigV2{agentV3Chat},
				AgentV3: &config.AgentV3Config{
					Enable:  true,
					Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
					Skills:  config.AgentV3SkillsConfig{Mode: "system_prompt"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.BotConfig = tt.botConfig

			err := validateAgentV3StartupConfig()

			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, `agent v3 chat "agent-v3" cannot use runtime`)
			assert.ErrorContains(t, err, tt.wantContains)
		})
	}
}

func snapshotCompiledChats() map[string]*CompiledChat {
	chats := make(map[string]*CompiledChat)
	compiledChats.Range(func(key, value any) bool {
		chats[key.(string)] = value.(*CompiledChat)
		return true
	})
	return chats
}

func restoreCompiledChats(chats map[string]*CompiledChat) {
	clearCompiledChats()
	for name, chat := range chats {
		compiledChats.Store(name, chat)
	}
}

func clearCompiledChats() {
	compiledChats.Range(func(key, _ any) bool {
		compiledChats.Delete(key)
		return true
	})
}
