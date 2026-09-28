package config

import (
	"errors"
	"fmt"
	"time"
	_ "time/tzdata"
)

const (
	defaultCronPollIntervalMinutes = 1
	defaultCronTimezone            = "Asia/Shanghai"
	defaultCronMaxConcurrency      = 4
	defaultCronChatCooldown        = 60
	defaultCronMaxTasksPerChat     = 20
	defaultCronMaxPromptBytes      = 16384
	defaultCronMaxManualRetries    = 2
	defaultCronRunTimeout          = "10m"
)

var (
	errCronPollInterval   = errors.New("agent_v3.cron.poll_interval_minutes must be between 1 and 60")
	errCronMaxConcurrency = errors.New("agent_v3.cron.max_concurrency must be between 1 and 64")
	errCronChatCooldown   = errors.New("agent_v3.cron.chat_cooldown_seconds must be between 1 and 86400")
	errCronMaxTasks       = errors.New("agent_v3.cron.max_tasks_per_chat must be between 1 and 1000")
	errCronMaxPromptBytes = errors.New("agent_v3.cron.max_prompt_bytes must be between 256 and 65536")
	errCronMaxRetries     = errors.New("agent_v3.cron.max_manual_retries must be between 1 and 10")
	errCronRunTimeout     = errors.New("agent_v3.cron.run_timeout must be a duration between 1m and 24h")
)

// AgentV3CronConfig configures scheduled Agent v3 runs and their limits.
type AgentV3CronConfig struct {
	RunnerAgent         string `mapstructure:"runner_agent"`
	PollIntervalMinutes int    `mapstructure:"poll_interval_minutes"`
	Timezone            string `mapstructure:"timezone"`
	MaxConcurrency      int    `mapstructure:"max_concurrency"`
	ChatCooldownSeconds int    `mapstructure:"chat_cooldown_seconds"`
	MaxTasksPerChat     int    `mapstructure:"max_tasks_per_chat"`
	MaxPromptBytes      int    `mapstructure:"max_prompt_bytes"`
	MaxManualRetries    int    `mapstructure:"max_manual_retries"`
	RunTimeout          string `mapstructure:"run_timeout"`
}

// DefaultAgentV3CronConfig returns the default cron runtime configuration.
func DefaultAgentV3CronConfig() AgentV3CronConfig {
	return AgentV3CronConfig{
		PollIntervalMinutes: defaultCronPollIntervalMinutes,
		Timezone:            defaultCronTimezone,
		MaxConcurrency:      defaultCronMaxConcurrency,
		ChatCooldownSeconds: defaultCronChatCooldown,
		MaxTasksPerChat:     defaultCronMaxTasksPerChat,
		MaxPromptBytes:      defaultCronMaxPromptBytes,
		MaxManualRetries:    defaultCronMaxManualRetries,
		RunTimeout:          defaultCronRunTimeout,
	}
}

// WithDefaults fills unset cron options with their defaults.
func (c AgentV3CronConfig) WithDefaults() AgentV3CronConfig {
	defaults := DefaultAgentV3CronConfig()
	if c.PollIntervalMinutes == 0 {
		c.PollIntervalMinutes = defaults.PollIntervalMinutes
	}
	if c.Timezone == "" {
		c.Timezone = defaults.Timezone
	}
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = defaults.MaxConcurrency
	}
	if c.ChatCooldownSeconds == 0 {
		c.ChatCooldownSeconds = defaults.ChatCooldownSeconds
	}
	if c.MaxTasksPerChat == 0 {
		c.MaxTasksPerChat = defaults.MaxTasksPerChat
	}
	if c.MaxPromptBytes == 0 {
		c.MaxPromptBytes = defaults.MaxPromptBytes
	}
	if c.MaxManualRetries == 0 {
		c.MaxManualRetries = defaults.MaxManualRetries
	}
	if c.RunTimeout == "" {
		c.RunTimeout = defaults.RunTimeout
	}
	return c
}

// Validate checks cron timing and resource limits.
func (c AgentV3CronConfig) Validate() error {
	if c.PollIntervalMinutes < 1 || c.PollIntervalMinutes > 60 {
		return errCronPollInterval
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("agent_v3.cron.timezone is invalid: %w", err)
	}
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 64 {
		return errCronMaxConcurrency
	}
	if c.ChatCooldownSeconds < 1 || c.ChatCooldownSeconds > 86400 {
		return errCronChatCooldown
	}
	if c.MaxTasksPerChat < 1 || c.MaxTasksPerChat > 1000 {
		return errCronMaxTasks
	}
	if c.MaxPromptBytes < 256 || c.MaxPromptBytes > 64*1024 {
		return errCronMaxPromptBytes
	}
	if c.MaxManualRetries < 1 || c.MaxManualRetries > 10 {
		return errCronMaxRetries
	}
	runTimeout, err := time.ParseDuration(c.RunTimeout)
	if err != nil || runTimeout < time.Minute || runTimeout > 24*time.Hour {
		return errCronRunTimeout
	}
	return nil
}

// RunTimeoutDuration parses the configured run timeout, returning zero when invalid.
func (c AgentV3CronConfig) RunTimeoutDuration() time.Duration {
	duration, err := time.ParseDuration(c.RunTimeout)
	if err != nil {
		return 0
	}
	return duration
}

// CronConfig returns the Agent v3 cron configuration with defaults applied.
func (c *AgentV3Config) CronConfig() AgentV3CronConfig {
	if c == nil {
		return DefaultAgentV3CronConfig()
	}
	return c.Cron.WithDefaults()
}

// ValidateCron validates the effective Agent v3 cron configuration.
func (c *AgentV3Config) ValidateCron() error {
	return c.CronConfig().Validate()
}
