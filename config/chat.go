package config

type chatConfig struct {
	Key           string  `koanf:"key"`
	MaxTokens     int     `koanf:"max_tokens"`
	Temperature   float32 `koanf:"temperature"`
	PromptLimit   int     `koanf:"prompt_limit"`
	SystemPrompt  string  `koanf:"system_prompt"`
	KeepContext   int     `koanf:"keep_context"`
	Model         string  `koanf:"model"`
	RetryNums     int     `koanf:"retry_nums"`
	RetryInterval int     `koanf:"retry_interval"`
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
	if c.RetryNums < 1 {
		c.RetryNums = 1
	}
}
