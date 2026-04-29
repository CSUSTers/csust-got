package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
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
