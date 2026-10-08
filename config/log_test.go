package config

import (
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestLogConfigDefaults(t *testing.T) {
	cfg := &LogConfig{}
	cfg.checkConfig()
	require.Equal(t, &LogConfig{MaxSizeMB: 100, MaxBackups: 7, MaxAgeDays: 14}, cfg)
	require.True(t, cfg.CompressEnabled())
	require.True(t, (*LogConfig)(nil).CompressEnabled())
}

func TestLogConfigReadConfig(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("log.max_size_mb", 5)
	viper.Set("log.max_backups", 2)
	viper.Set("log.max_age_days", 3)
	viper.Set("log.compress", false)
	cfg := &LogConfig{}
	cfg.readConfig()
	cfg.checkConfig()
	require.Equal(t, 5, cfg.MaxSizeMB)
	require.Equal(t, 2, cfg.MaxBackups)
	require.Equal(t, 3, cfg.MaxAgeDays)
	require.False(t, cfg.CompressEnabled())
}

func TestAgentV3ObservabilityAndShutdownDefaults(t *testing.T) {
	cfg := &AgentV3Config{}
	cfg.checkConfig()
	require.Equal(t, 50, cfg.Observability.TraceMaxSizeMB)
	require.Equal(t, 5, cfg.Observability.TraceMaxBackups)
	require.Equal(t, 256, cfg.Observability.TraceQueue)
	require.Equal(t, 60*time.Second, cfg.ShutdownGraceDuration())
	require.Equal(t, 60*time.Second, (*AgentV3Config)(nil).ShutdownGraceDuration())

	cfg.ShutdownGrace = "15s"
	require.Equal(t, 15*time.Second, cfg.ShutdownGraceDuration())
	cfg.ShutdownGrace = "bogus"
	require.Equal(t, 60*time.Second, cfg.ShutdownGraceDuration())
}
