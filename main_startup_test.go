package main

import (
	"os"
	"path/filepath"
	"testing"

	"csust-got/config"
	"csust-got/log"

	"github.com/samber/lo"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestStartupLogsConfigDiagnosticsAfterInitLogger(t *testing.T) {
	originalConfig, originalGlobal := config.BotConfig, zap.L()
	t.Cleanup(func() {
		config.BotConfig = originalConfig
		zap.ReplaceGlobals(originalGlobal)
		viper.Reset()
	})
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte("token: startup-token\nredis:\n  addr: localhost:6379\nlog:\n  max_size_mbb: 5\nagent_v3:\n  model:\n    prompt_limit: 10\n"), 0o644))

	zap.ReplaceGlobals(zap.NewNop())
	config.InitConfig(configFile, "BOT_STARTUP_TEST")
	require.Equal(t, []config.Diagnostic{
		{Path: "log.max_size_mbb", Kind: config.DiagnosticUnknownKey},
		{Path: "agent_v3.model.prompt_limit", Kind: config.DiagnosticUnimplementedKey},
	}, config.Diagnostics())

	log.InitLogger()
	t.Cleanup(log.Close)
	core, logs := observer.New(zap.WarnLevel)
	zap.ReplaceGlobals(zap.L().WithOptions(zap.WrapCore(func(c zapcore.Core) zapcore.Core { return zapcore.NewTee(c, core) })))
	config.LogDiagnostics()

	paths := func(msg string) []string {
		return lo.Map(logs.FilterMessage(msg).All(), func(e observer.LoggedEntry, _ int) string {
			return e.ContextMap()["path"].(string)
		})
	}
	require.Equal(t, []string{"log.max_size_mbb"}, paths("unknown config key"))
	require.Equal(t, []string{"agent_v3.model.prompt_limit"}, paths("config key is read but not implemented"))
}
