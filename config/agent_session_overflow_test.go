package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAgentV3SessionOverflowConfig(t *testing.T) {
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(restore)
	for _, raw := range []string{
		"enable: false", "context_overflow: {}",
		"context_overflow: {strategy: rebuild, max_tokens: 1}",
		"context_overflow: {max_tokens: 9223372036854775807}",
	} {
		t.Run(raw, func(t *testing.T) {
			v := viper.New()
			v.SetConfigType("yaml")
			require.NoError(t, v.ReadConfig(strings.NewReader("agent_v3:\n  session:\n    "+raw)))
			var cfg AgentV3Config
			require.NoError(t, v.UnmarshalKey("agent_v3", &cfg, viper.DecodeHook(DispatchFor())))
			require.NoError(t, cfg.Session.Validate())
			require.Equal(t, "rebuild", cfg.Session.ContextOverflow.StrategyName())
		})
	}
	for _, field := range []string{
		"enable: ''", "enable: 0", "enable: 1", "enable: 0.0", "enable: 1.0", "enable: []", "enable: {}", "enable: null",
		"context_overflow: []", "context_overflow: false", "context_overflow: ''",
		"context_overflow: {strategy: ''}", "context_overflow: {strategy: trim}", "context_overflow: {strategy: false}",
		"context_overflow: {max_tokens: 0}", "context_overflow: {max_tokens: -1}",
		"context_overflow: {max_tokens: 200000.5}", "context_overflow: {max_tokens: 200000.0}",
		"context_overflow: {max_tokens: false}", "context_overflow: {max_tokens: []}",
		"context_overflow: {max_tokens: {value: 1}}", "context_overflow: {max_tokens: ''}",
		"context_overflow: {max_tokens: null}", "context_overflow: {max_tokens: 9223372036854775808}",
	} {
		t.Run(field, func(t *testing.T) {
			v := viper.New()
			v.SetConfigType("yaml")
			require.NoError(t, v.ReadConfig(strings.NewReader("agent_v3:\n  session:\n    "+field)))
			var cfg AgentV3Config
			require.NoError(t, v.UnmarshalKey("agent_v3", &cfg, viper.DecodeHook(DispatchFor())))
			require.Error(t, cfg.Session.Validate())
			require.PanicsWithValue(t, "invalid agent_v3 session config", cfg.checkConfig)
		})
	}
	var defaults AgentV3SessionConfig
	require.NoError(t, defaults.Validate())
	require.True(t, defaults.Enabled())
	require.EqualValues(t, 200000, defaults.ContextOverflow.TokenLimit())
}

func TestAgentV3SessionNewEnvironmentKeys(t *testing.T) {
	originalEnv, originalErr := runtimeEnvConfig, errRuntimeEnvConfigState
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(func() {
		runtimeEnvConfig, errRuntimeEnvConfigState = originalEnv, originalErr
		viper.Reset()
		restore()
	})
	for _, limit := range []string{"200001", "0", "-1", "1.5", "false", "{}", "[]", "", "9223372036854775808"} {
		t.Run(limit, func(t *testing.T) {
			viper.Reset()
			viper.SetConfigType("yaml")
			require.NoError(t, viper.ReadConfig(strings.NewReader("agent_v3: {}")))
			t.Setenv("SESSION_TEST_AGENT_V3_SESSION_ENABLE", "false")
			t.Setenv("SESSION_TEST_AGENT_V3_SESSION_CONTEXT_OVERFLOW_STRATEGY", "rebuild")
			t.Setenv("SESSION_TEST_AGENT_V3_SESSION_CONTEXT_OVERFLOW_MAX_TOKENS", limit)
			InitViper("", "SESSION_TEST")
			var cfg AgentV3Config
			cfg.readConfig()
			require.NotNil(t, cfg.Session.Enable)
			require.False(t, cfg.Session.Enabled())
			if limit == "200001" {
				require.NoError(t, cfg.Session.Validate())
				require.EqualValues(t, 200001, cfg.Session.ContextOverflow.TokenLimit())
			} else {
				require.Error(t, cfg.Session.Validate(), "disabled does not hide invalid configuration")
			}
		})
	}
	for _, enable := range []string{"", "invalid"} {
		viper.Reset()
		t.Setenv("SESSION_TEST_AGENT_V3_SESSION_ENABLE", enable)
		InitViper("", "SESSION_TEST")
		var cfg AgentV3Config
		cfg.readConfig()
		require.Error(t, cfg.Session.Validate())
	}
}

func TestAgentV3SessionEnableParseBoolStrings(t *testing.T) {
	for _, text := range []string{"true", "TRUE", "True", "1", "t", "T", "false", "FALSE", "False", "0", "f", "F"} {
		t.Run(text, func(t *testing.T) {
			v := viper.New()
			v.SetConfigType("yaml")
			require.NoError(t, v.ReadConfig(strings.NewReader("agent_v3: {session: {enable: '"+text+"'}}")))
			var cfg AgentV3Config
			require.NoError(t, v.UnmarshalKey("agent_v3", &cfg, viper.DecodeHook(DispatchFor())))
			require.NoError(t, cfg.Session.Validate())
			require.Equal(t, text == "true" || text == "TRUE" || text == "True" || text == "1" || text == "t" || text == "T", cfg.Session.Enabled())
		})
	}
}

func TestAgentV3SessionInvalidSurvivesUnmarshalWarning(t *testing.T) {
	originalEnv, originalErr := runtimeEnvConfig, errRuntimeEnvConfigState
	restore := zap.ReplaceGlobals(zap.NewNop())
	viper.Reset()
	t.Cleanup(func() {
		runtimeEnvConfig, errRuntimeEnvConfigState = originalEnv, originalErr
		viper.Reset()
		restore()
	})
	InitViper("", "SESSION_TEST")
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader("agent_v3:\n  runtime:\n    enable: {}\n  session:\n    enable: false\n    context_overflow:\n      max_tokens: 200000.5\n")))
	var cfg AgentV3Config
	cfg.readConfig()
	require.Error(t, cfg.Session.Validate())
	require.PanicsWithValue(t, "invalid agent_v3 session config", cfg.checkConfig)
}

func TestAgentV3SessionParentShapeFromReadConfig(t *testing.T) {
	originalEnv, originalErr := runtimeEnvConfig, errRuntimeEnvConfigState
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(func() {
		runtimeEnvConfig, errRuntimeEnvConfigState = originalEnv, originalErr
		viper.Reset()
		restore()
	})
	for _, test := range []struct {
		yaml    string
		invalid bool
	}{
		{"{}", false},
		{"agent_v3: {}", false},
		{"agent_v3: {session: {}}", false},
		{"agent_v3: {session: {enable: false, context_overflow: {max_tokens: 200001}}}", false},
		{"agent_v3: null", true},
		{"agent_v3: false", true},
		{"agent_v3: []", true},
		{"agent_v3: {session: null}", true},
		{"agent_v3: {session: false}", true},
		{"agent_v3: {session: []}", true},
		{"agent_v3: {session: ''}", true},
		{"agent_v3: {runtime: {enable: {}}, session: null}", true},
	} {
		t.Run(test.yaml, func(t *testing.T) {
			viper.Reset()
			InitViper("", "SESSION_SHAPE_TEST")
			viper.SetConfigType("yaml")
			require.NoError(t, viper.ReadConfig(strings.NewReader(test.yaml)))
			var cfg AgentV3Config
			cfg.readConfig()
			if test.invalid {
				require.Error(t, cfg.Session.Validate(), "explicit null/non-object is not omission")
				require.PanicsWithValue(t, "invalid agent_v3 session config", cfg.checkConfig)
			} else {
				require.NoError(t, cfg.Session.Validate())
				require.NotPanics(t, cfg.checkConfig)
			}
		})
	}
}

func TestAgentV3SessionNullFailsRealInitConfig(t *testing.T) {
	originalConfig := BotConfig
	originalEnv, originalErr := runtimeEnvConfig, errRuntimeEnvConfigState
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(func() {
		BotConfig = originalConfig
		runtimeEnvConfig, errRuntimeEnvConfigState = originalEnv, originalErr
		viper.Reset()
		restore()
	})
	for _, section := range []string{"agent_v3: {enable: false, session: null}", "agent_v3: {enable: false, session: {}}", "agent_v3: {enable: false}"} {
		t.Run(section, func(t *testing.T) {
			viper.Reset()
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte("token: test-token\nredis: {addr: localhost:6379}\n"+section+"\n"), 0o600))
			if strings.Contains(section, "null") {
				require.PanicsWithValue(t, "invalid agent_v3 session config", func() { InitConfig(path, "SESSION_SHAPE_TEST") })
			} else {
				require.NotPanics(t, func() { InitConfig(path, "SESSION_SHAPE_TEST") })
				require.True(t, BotConfig.AgentV3.Session.Enabled())
				require.EqualValues(t, 200000, BotConfig.AgentV3.Session.ContextOverflow.TokenLimit())
			}
		})
	}
}
