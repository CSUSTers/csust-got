package config

import "github.com/spf13/viper"

type chatConfig struct {
	Key           string
	MaxTokens     int
	Temperature   float32
	PromptLimit   int
	SystemPrompt  string
	KeepContext   int
	Model         string
	RetryNums     int
	RetryInterval int
}

func (c *chatConfig) readConfig() {
	c.Key = viper.GetString("chatgpt.key")
	c.MaxTokens = viper.GetInt("chatgpt.max_tokens")
	c.Temperature = float32(viper.GetFloat64("chatgpt.temperature"))
	c.PromptLimit = viper.GetInt("chatgpt.prompt_limit")
	c.SystemPrompt = viper.GetString("chatgpt.system_prompt")
	c.KeepContext = viper.GetInt("chatgpt.keep_context")
	c.Model = viper.GetString("chatgpt.model")
	c.RetryNums = viper.GetInt("chatgpt.retry_nums")
	c.RetryInterval = viper.GetInt("chatgpt.retry_interval")
}

func (c *chatConfig) checkConfig() {
	if c.MaxTokens <= 0 {
		c.MaxTokens = 10
	}
	if c.Temperature < 0 || c.Temperature > 2 {
		c.Temperature = 1
	}
	if c.PromptLimit <= 0 {
		c.PromptLimit = 10
	}
	if c.KeepContext < 0 {
		c.KeepContext = 0
	}
}
