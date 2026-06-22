package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatConfigV1_ReadConfig(t *testing.T) {
	const config = `
models:
  - &gpt
    name: gpt-3.5
    base_url: "http://test.com"
    api_key: &key "test-key"
  - &qwen
    name: qwen3:32b
    base_url: "http://test.com"
    api_key: *key
chats:
  - &c1 
    name: test
    message_context: 5
    system_prompt: "test prompt"
    model: *gpt
  - <<: *c1
    system_prompt:
    - "line 1\n"
    - |
      line 2
    - 'line3
	  line3'
    - "
	  line3"
    - >+

      line4
      line4

    - |-
      line6
      line7
`
	viper.SetConfigType("yaml")
	assert.NoError(t, viper.ReadConfig(strings.NewReader(config)))

	var c ChatConfigV1
	c.readConfig()
	assert.Len(t, c, 2)
	assert.Equal(t, ChatConfigV1{
		&ChatConfigSingle{
			Name:           "test",
			MessageContext: 5,
			SystemPrompt:   "test prompt",
			Model:          &Model{Name: "gpt-3.5", BaseUrl: "http://test.com", ApiKey: "test-key"},
		},
		&ChatConfigSingle{
			Name:           "test",
			MessageContext: 5,
			SystemPrompt:   "line 1\nline 2\nline3 line3 line3\nline4 line4\n\nline6\nline7",
			Model:          &Model{Name: "gpt-3.5", BaseUrl: "http://test.com", ApiKey: "test-key"},
		},
	}, c)
}

func TestAgentGetMaxSteps(t *testing.T) {
	t.Run("tool-enabled main agent clamps too-low max steps", func(t *testing.T) {
		cfg := &AgentConfig{
			MaxSteps: 1,
			Tools:    []string{"update_progress"},
		}
		assert.Equal(t, minToolAgentMaxSteps, cfg.GetMaxSteps())
	})

	t.Run("tool-enabled subagent clamps too-low max steps", func(t *testing.T) {
		cfg := &SubAgentConfig{
			MaxSteps: 2,
			Tools:    []string{"get_context"},
		}
		assert.Equal(t, minToolAgentMaxSteps, cfg.GetMaxSteps())
	})

	t.Run("tool-free agent preserves explicit low max steps", func(t *testing.T) {
		cfg := &AgentConfig{MaxSteps: 1}
		assert.Equal(t, 1, cfg.GetMaxSteps())
	})

	t.Run("default values stay unchanged when max steps unset", func(t *testing.T) {
		assert.Equal(t, defaultAgentMaxSteps, (&AgentConfig{}).GetMaxSteps())
		assert.Equal(t, defaultSubAgentMaxSteps, (&SubAgentConfig{}).GetMaxSteps())
	})
}

func TestAgentRichConfigParsesFromChatV2(t *testing.T) {
	const raw = `
agents:
  - name: rich-agent
    agent:
      enable: true
      v3: true
      rich: true
`
	v := viper.New()
	v.SetConfigType("yaml")
	assert.NoError(t, v.ReadConfig(strings.NewReader(raw)))

	var cfg ChatConfigV2
	assert.NoError(t, v.UnmarshalKey("agents", &cfg, viper.DecodeHook(DispatchFor())))

	if assert.Len(t, cfg, 1) && assert.NotNil(t, cfg[0].Agent) {
		assert.True(t, cfg[0].Agent.Rich)
	}
}

func TestIsAgentV3RichEnabledRequiresAgentV3AndRichGate(t *testing.T) {
	old := BotConfig
	t.Cleanup(func() { BotConfig = old })
	BotConfig = &Config{AgentV3: &AgentV3Config{Enable: true}}

	tests := []struct {
		name string
		cfg  *ChatConfigSingle
		want bool
	}{
		{name: "nil config is false"},
		{
			name: "missing agent is false",
			cfg:  &ChatConfigSingle{},
		},
		{
			name: "rich without agent enable is false",
			cfg:  &ChatConfigSingle{Agent: &AgentConfig{Rich: true}},
		},
		{
			name: "v3 without rich is false",
			cfg:  &ChatConfigSingle{Agent: &AgentConfig{Enable: true, V3: true}},
		},
		{
			name: "global v3 plus rich is true",
			cfg:  &ChatConfigSingle{Agent: &AgentConfig{Enable: true, Rich: true}},
			want: true,
		},
		{
			name: "per chat v3 plus rich is true",
			cfg:  &ChatConfigSingle{Agent: &AgentConfig{Enable: true, V3: true, Rich: true}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cfg.IsAgentV3RichEnabled())
		})
	}
}

func TestAgentV3CheckConfigNormalizesFixedRuntimeSurface(t *testing.T) {
	cfg := &AgentV3Config{
		Memory: AgentV3MemoryConfig{
			Scope:       "global",
			AllowGlobal: true,
			WritePolicy: "model_auto",
		},
		Runtime: AgentV3RuntimeConfig{
			Mode:           "host",
			NamespaceScope: "global",
		},
		Tools: AgentV3ToolsConfig{
			ExposeOnly: []string{"read", "bash", "mcp_search"},
		},
		Skills: AgentV3SkillsConfig{
			Mode: "runtime_filesystem",
			Root: "/tmp/skills",
		},
	}
	cfg.checkConfig()

	assert.Equal(t, "group", cfg.Memory.Scope)
	assert.False(t, cfg.Memory.AllowGlobal)
	assert.Equal(t, "explicit_or_admin", cfg.Memory.WritePolicy)
	assert.Equal(t, "remote_http", cfg.Runtime.Mode)
	assert.Equal(t, "group", cfg.Runtime.NamespaceScope)
	assert.Equal(t, []string{"read", "grep", "write", "edit", "bash"}, cfg.Tools.ExposeOnly)
	assert.Equal(t, "system_prompt", cfg.Skills.Mode)
	assert.Empty(t, cfg.Skills.Root)
	require.NotNil(t, cfg.Skills.InjectBuiltin)
	assert.True(t, *cfg.Skills.InjectBuiltin)
}
