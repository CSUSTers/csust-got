//go:build !386 && !arm

package agentv3

import (
	"path/filepath"
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitRejectsInvalidAgentV3RuntimeBeforeCompilation(t *testing.T) {
	oldConfig := config.BotConfig
	oldMcpManager := mcpManager
	oldCompiledAgents := snapshotCompiledAgents()
	t.Cleanup(func() {
		config.BotConfig = oldConfig
		mcpManager = oldMcpManager
		restoreCompiledAgents(oldCompiledAgents)
	})

	config.BotConfig = &config.Config{
		Agents: &config.AgentV3Configs{
			{Name: "invalid-agent-v3", Agent: &config.AgentOptions{Enable: true}},
		},
		AgentV3: &config.AgentV3Config{Enable: true},
	}
	mcpManager = nil
	clearCompiledAgents()
	compiledAgents.Store("existing", &CompiledAgent{})

	err := Init(t.Context())

	require.Error(t, err)
	assert.ErrorContains(t, err, `agent "invalid-agent-v3" cannot use runtime`)
	assert.ErrorContains(t, err, "runtime is disabled")
	assert.ErrorIs(t, err, errAgentV3RuntimeDisabled)
	assert.Nil(t, mcpManager)
	assert.True(t, HasCompiledAgent("existing"))
	assert.False(t, HasCompiledAgent("invalid-agent-v3"))
}

func TestInitRejectsInvalidSkillSourcesBeforeMCPAndCompilation(t *testing.T) {
	oldConfig := config.BotConfig
	oldMcpManager := mcpManager
	oldCompiledAgents := snapshotCompiledAgents()
	t.Cleanup(func() {
		config.BotConfig = oldConfig
		mcpManager = oldMcpManager
		restoreCompiledAgents(oldCompiledAgents)
	})

	existingManager := NewMcpManager()
	config.BotConfig = &config.Config{
		Agents: &config.AgentV3Configs{
			{Name: "invalid-skills", Agent: &config.AgentOptions{Enable: true}},
		},
		AgentV3: &config.AgentV3Config{
			Enable:  true,
			Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
			Skills: config.AgentV3SkillsConfig{
				Mode: "system_prompt",
				Root: filepath.Join(t.TempDir(), "missing-skills"),
			},
		},
	}
	mcpManager = existingManager
	clearCompiledAgents()
	compiledAgents.Store("existing", &CompiledAgent{})

	err := Init(t.Context())

	require.Error(t, err)
	assert.ErrorContains(t, err, "lstat skills root")
	assert.Same(t, existingManager, mcpManager)
	assert.True(t, HasCompiledAgent("existing"))
	assert.False(t, HasCompiledAgent("invalid-skills"))
}

func TestInitReturnsEnabledAgentCompilationFailure(t *testing.T) {
	oldConfig := config.BotConfig
	oldMcpManager := mcpManager
	oldCompiledAgents := snapshotCompiledAgents()
	t.Cleanup(func() {
		if mcpManager != nil && mcpManager != oldMcpManager {
			mcpManager.Close()
		}
		config.BotConfig = oldConfig
		mcpManager = oldMcpManager
		restoreCompiledAgents(oldCompiledAgents)
	})

	injectBuiltin := false
	config.BotConfig = &config.Config{
		Agents: &config.AgentV3Configs{
			{
				Name:         "broken-template-agent",
				SystemPrompt: config.JoinableString("{{"),
				Agent:        &config.AgentOptions{Enable: true},
			},
		},
		AgentV3: &config.AgentV3Config{
			Enable:  true,
			Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
			Skills: config.AgentV3SkillsConfig{
				Mode:          "system_prompt",
				InjectBuiltin: &injectBuiltin,
			},
		},
	}
	mcpManager = nil
	clearCompiledAgents()

	err := Init(t.Context())

	require.Error(t, err)
	assert.ErrorContains(t, err, `compile agent "broken-template-agent"`)
	assert.ErrorContains(t, err, "failed to parse system prompt template")
	assert.False(t, HasCompiledAgent("broken-template-agent"))
}

func TestValidateAgentV3StartupConfig(t *testing.T) {
	oldConfig := config.BotConfig
	t.Cleanup(func() { config.BotConfig = oldConfig })

	enabledAgent := &config.AgentConfig{
		Name:  "agent-v3",
		Agent: &config.AgentOptions{Enable: true},
	}
	disabledAgent := &config.AgentConfig{
		Name:  "disabled-agent",
		Agent: &config.AgentOptions{},
	}
	injectBuiltin := false

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
				Agents: &config.AgentV3Configs{enabledAgent},
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
				Agents:  &config.AgentV3Configs{enabledAgent},
				AgentV3: &config.AgentV3Config{},
			},
		},
		{
			name: "no enabled agents",
			botConfig: &config.Config{
				Agents:  &config.AgentV3Configs{disabledAgent},
				AgentV3: &config.AgentV3Config{Enable: true},
			},
		},
		{
			name: "disabled runtime",
			botConfig: &config.Config{
				Agents:  &config.AgentV3Configs{enabledAgent},
				AgentV3: &config.AgentV3Config{Enable: true},
			},
			wantErr:      true,
			wantContains: "runtime is disabled",
		},
		{
			name: "valid remote HTTP runtime and system prompt skills",
			botConfig: &config.Config{
				Agents: &config.AgentV3Configs{enabledAgent},
				AgentV3: &config.AgentV3Config{
					Enable:  true,
					Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
					Skills:  config.AgentV3SkillsConfig{Mode: "system_prompt"},
				},
			},
		},
		{
			name: "invalid SearXNG ignored when built-ins disabled",
			botConfig: &config.Config{
				Agents: &config.AgentV3Configs{enabledAgent},
				AgentV3: &config.AgentV3Config{
					Enable:  true,
					Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
					Skills: config.AgentV3SkillsConfig{
						Mode:          "system_prompt",
						InjectBuiltin: &injectBuiltin,
						SearXNG:       config.AgentV3SearXNGConfig{Enable: true},
					},
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
			assert.ErrorContains(t, err, `agent "agent-v3" cannot use runtime`)
			assert.ErrorContains(t, err, tt.wantContains)
		})
	}

	config.BotConfig = &config.Config{
		Agents: &config.AgentV3Configs{enabledAgent},
		AgentV3: &config.AgentV3Config{
			Enable:  true,
			Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"},
			Skills: config.AgentV3SkillsConfig{
				Mode:    "system_prompt",
				SearXNG: config.AgentV3SearXNGConfig{Enable: true},
			},
		},
	}
	err := validateAgentV3StartupConfig()
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid SearXNG config")
	assert.ErrorContains(t, err, "agent_v3.skills.searxng.base_url")
}

func snapshotCompiledAgents() map[string]*CompiledAgent {
	agents := make(map[string]*CompiledAgent)
	compiledAgents.Range(func(key, value any) bool {
		agents[key.(string)] = value.(*CompiledAgent)
		return true
	})
	return agents
}

func restoreCompiledAgents(agents map[string]*CompiledAgent) {
	clearCompiledAgents()
	for name, agent := range agents {
		compiledAgents.Store(name, agent)
	}
}

func clearCompiledAgents() {
	compiledAgents.Range(func(key, _ any) bool {
		compiledAgents.Delete(key)
		return true
	})
}
