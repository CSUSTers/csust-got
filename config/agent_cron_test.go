package config

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentV3CronDefaults(t *testing.T) {
	defaults := DefaultAgentV3CronConfig()
	assert.Empty(t, defaults.RunnerAgent)
	assert.Equal(t, 1, defaults.PollIntervalMinutes)
	assert.Equal(t, "Asia/Shanghai", defaults.Timezone)
	assert.Equal(t, 4, defaults.MaxConcurrency)
	assert.Equal(t, 60, defaults.ChatCooldownSeconds)
	assert.Equal(t, 20, defaults.MaxTasksPerChat)
	assert.Equal(t, 16384, defaults.MaxPromptBytes)
	assert.Equal(t, 2, defaults.MaxManualRetries)
	assert.Equal(t, "10m", defaults.RunTimeout)
	require.NoError(t, defaults.Validate())
	assert.Equal(t, 10*time.Minute, defaults.RunTimeoutDuration())

	var nilConfig *AgentV3Config
	assert.Equal(t, defaults, nilConfig.CronConfig())
}

func TestAgentV3CronConfigUnmarshalAndPreserveOverrides(t *testing.T) {
	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(`
agent_v3:
  cron:
    runner_agent: cron-runner
    poll_interval_minutes: 2
    timezone: UTC
    max_concurrency: 8
    chat_cooldown_seconds: 30
    max_tasks_per_chat: 40
    max_prompt_bytes: 32768
    max_manual_retries: 3
    run_timeout: 20m
`)))

	var cfg AgentV3Config
	require.NoError(t, v.UnmarshalKey("agent_v3", &cfg, viper.DecodeHook(DispatchFor())))
	cfg.checkConfig()
	require.NoError(t, cfg.ValidateCron())
	assert.Equal(t, "cron-runner", cfg.Cron.RunnerAgent)
	assert.Equal(t, 2, cfg.Cron.PollIntervalMinutes)
	assert.Equal(t, "UTC", cfg.Cron.Timezone)
	assert.Equal(t, 8, cfg.Cron.MaxConcurrency)
	assert.Equal(t, 20*time.Minute, cfg.Cron.RunTimeoutDuration())
}

func TestAgentV3CronValidationRejectsOutOfBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AgentV3CronConfig)
	}{
		{name: "poll interval", mutate: func(c *AgentV3CronConfig) { c.PollIntervalMinutes = 61 }},
		{name: "timezone", mutate: func(c *AgentV3CronConfig) { c.Timezone = "Mars/Olympus" }},
		{name: "concurrency", mutate: func(c *AgentV3CronConfig) { c.MaxConcurrency = 65 }},
		{name: "cooldown", mutate: func(c *AgentV3CronConfig) { c.ChatCooldownSeconds = 86401 }},
		{name: "tasks", mutate: func(c *AgentV3CronConfig) { c.MaxTasksPerChat = 1001 }},
		{name: "prompt bytes low", mutate: func(c *AgentV3CronConfig) { c.MaxPromptBytes = 255 }},
		{name: "prompt bytes high", mutate: func(c *AgentV3CronConfig) { c.MaxPromptBytes = 64*1024 + 1 }},
		{name: "retries", mutate: func(c *AgentV3CronConfig) { c.MaxManualRetries = 11 }},
		{name: "timeout malformed", mutate: func(c *AgentV3CronConfig) { c.RunTimeout = "soon" }},
		{name: "timeout low", mutate: func(c *AgentV3CronConfig) { c.RunTimeout = "59s" }},
		{name: "timeout high", mutate: func(c *AgentV3CronConfig) { c.RunTimeout = "24h1s" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultAgentV3CronConfig()
			tt.mutate(&cfg)
			require.Error(t, cfg.Validate())
		})
	}
}

func TestAgentV3CheckConfigAppliesCronDefaults(t *testing.T) {
	cfg := &AgentV3Config{}
	cfg.checkConfig()
	assert.Equal(t, DefaultAgentV3CronConfig(), cfg.Cron)
}
