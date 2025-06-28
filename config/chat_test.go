package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestChatConfigV2_ReadConfig(t *testing.T) {
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
    stream: true
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

	var c ChatConfigV2
	c.readConfig()
	assert.Len(t, c, 2)
	assert.Equal(t, ChatConfigV2{
		&ChatConfigSingle{
			Name:           "test",
			MessageContext: 5,
			Steam:          true,
			SystemPrompt:   "test prompt",
			Model:          &Model{Name: "gpt-3.5", BaseUrl: "http://test.com", ApiKey: "test-key"},
		},
		&ChatConfigSingle{
			Name:           "test",
			MessageContext: 5,
			Steam:          true,
			SystemPrompt:   "line 1\nline 2\nline3 line3 line3\nline4 line4\n\nline6\nline7",
			Model:          &Model{Name: "gpt-3.5", BaseUrl: "http://test.com", ApiKey: "test-key"},
		},
	}, c)
}
