package config

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAgentSessionTokenLimitOverrideStrict(t *testing.T) {
	for _, field := range []string{"", "context_overflow: {}", "context_overflow: {max_tokens: 1}", "context_overflow: {max_tokens: '128000'}", "context_overflow: {max_tokens: 9223372036854775807}"} {
		t.Run(field, func(t *testing.T) {
			v := viper.New()
			v.SetConfigType("yaml")
			require.NoError(t, v.ReadConfig(strings.NewReader("agents:\n  - name: limited\n    session:\n      "+field)))
			var agents AgentV3Configs
			require.NoError(t, v.UnmarshalKey("agents", &agents, viper.DecodeHook(DispatchFor())))
			require.Len(t, agents, 1)
			cfg := agents[0]
			require.NoError(t, cfg.Session.Validate())
			global := AgentV3SessionConfig{ContextOverflow: AgentV3SessionContextOverflowConfig{MaxTokens: 300000}}
			want := int64(300000)
			if cfg.Session.ContextOverflow.MaxTokens != nil {
				want = *cfg.Session.ContextOverflow.MaxTokens
			}
			require.Equal(t, want, cfg.EffectiveSessionTokenLimit(global))
			save, load := cfg.EffectiveSessionSettings(&AgentTrigger{Reply: true})
			require.True(t, save)
			require.True(t, load)
			require.Equal(t, want, cfg.EffectiveSessionTokenLimit(global), "actual reply inherits the agent limit")
		})
	}
	for _, field := range []string{"context_overflow: null", "context_overflow: []", "context_overflow: false", "context_overflow: ''", "context_overflow: {max_tokens: null}", "context_overflow: {max_tokens: 0}", "context_overflow: {max_tokens: -1}", "context_overflow: {max_tokens: 128000.0}", "context_overflow: {max_tokens: 1.5}", "context_overflow: {max_tokens: 9223372036854775808}", "context_overflow: {max_tokens: '9223372036854775808'}", "context_overflow: {max_tokens: false}", "context_overflow: {max_tokens: []}", "context_overflow: {max_tokens: {n: 1}}"} {
		t.Run(field, func(t *testing.T) {
			v := viper.New()
			v.SetConfigType("yaml")
			require.NoError(t, v.ReadConfig(strings.NewReader("agents:\n  - name: invalid\n    session:\n      save_context: false\n      load_context: false\n      "+field)))
			var agents AgentV3Configs
			require.NoError(t, v.UnmarshalKey("agents", &agents, viper.DecodeHook(DispatchFor())))
			require.Len(t, agents, 1)
			require.Error(t, agents[0].Session.Validate())
		})
	}
}

func TestAgentV3SessionStrictEnvironmentSchema(t *testing.T) {
	originalEnv, originalErr := runtimeEnvConfig, errRuntimeEnvConfigState
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(func() {
		runtimeEnvConfig, errRuntimeEnvConfigState = originalEnv, originalErr
		viper.Reset()
		restore()
	})
	for _, text := range []string{"TRUE", "False", "1", "0", "t", "f"} {
		t.Run(text, func(t *testing.T) {
			viper.Reset()
			viper.SetConfigType("yaml")
			require.NoError(t, viper.ReadConfig(strings.NewReader("{}")))
			t.Setenv("SESSION_REVIEW_AGENT_V3_SESSION_ENABLE", text)
			t.Setenv("SESSION_REVIEW_AGENT_V3_SESSION_DIRECTORY", "custom-sessions")
			t.Setenv("SESSION_REVIEW_AGENT_V3_SESSION_CONTEXT_OVERFLOW_MAX_TOKENS", "128000")
			InitViper("", "SESSION_REVIEW")
			var cfg AgentV3Config
			cfg.readConfig()
			require.NoError(t, cfg.Session.Validate())
			wantEnabled, err := strconv.ParseBool(text)
			require.NoError(t, err)
			require.Equal(t, wantEnabled, cfg.Session.Enabled())
			require.Equal(t, "custom-sessions", cfg.Session.DirectoryPath())
			require.EqualValues(t, 128000, cfg.Session.ContextOverflow.TokenLimit())
			require.Equal(t, 24*time.Hour, cfg.Session.IdleTTL())
		})
	}
}

func TestAgentV3SessionReadConfigYAMLBooleanStringContract(t *testing.T) {
	originalEnv, originalErr := runtimeEnvConfig, errRuntimeEnvConfigState
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(func() {
		runtimeEnvConfig, errRuntimeEnvConfigState = originalEnv, originalErr
		viper.Reset()
		restore()
	})
	for _, raw := range []string{"true", "false", "'true'", "'TRUE'", "'True'", "'1'", "'t'", "'T'", "'false'", "'FALSE'", "'False'", "'0'", "'f'", "'F'"} {
		t.Run("valid="+raw, func(t *testing.T) {
			viper.Reset()
			viper.SetConfigType("yaml")
			require.NoError(t, viper.ReadConfig(strings.NewReader("agent_v3: {session: {enable: "+raw+"}}")))
			InitViper("", "SESSION_YAML_REVIEW")
			var cfg AgentV3Config
			cfg.readConfig()
			require.NoError(t, cfg.Session.Validate())
			wantEnabled, err := strconv.ParseBool(strings.Trim(raw, "'"))
			require.NoError(t, err)
			require.Equal(t, wantEnabled, cfg.Session.Enabled())
			require.EqualValues(t, 200000, cfg.Session.ContextOverflow.TokenLimit())
		})
	}
	for _, raw := range []string{"1", "0", "1.0", "0.0", "null", "[]", "{}", "''", "'invalid'"} {
		t.Run("invalid="+raw, func(t *testing.T) {
			viper.Reset()
			viper.SetConfigType("yaml")
			require.NoError(t, viper.ReadConfig(strings.NewReader("agent_v3: {session: {enable: "+raw+"}}")))
			InitViper("", "SESSION_YAML_REVIEW")
			var cfg AgentV3Config
			cfg.readConfig()
			require.ErrorIs(t, cfg.Session.Validate(), errInvalidAgentV3SessionEnable)
		})
	}
}

func TestAgentV3SessionMixedKeysAndNullRemainInvalid(t *testing.T) {
	for _, raw := range []any{
		map[any]any{"enable": false, 1: "mixed"},
		map[string]any{"context_overflow": map[any]any{"max_tokens": 1, 2: false}},
		map[string]any{"context_overflow": nil},
		map[string]any{"enable": 1.0},
	} {
		decoded, err := (AgentV3SessionConfig{}).From(reflect.ValueOf(raw))
		require.NoError(t, err)
		require.Error(t, decoded.(AgentV3SessionConfig).Validate())
	}
}
