package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAgentV3SessionOverflowPatternsConfig(t *testing.T) {
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(restore)
	tests := []struct {
		name  string
		raw   string
		want  []string
		valid bool
	}{
		{"omitted", "context_overflow: {}", nil, true},
		{"list", "context_overflow: {patterns: ['maximum context length', 'prompt is too long']}", []string{"maximum context length", "prompt is too long"}, true},
		{"single string", "context_overflow: {patterns: 'Range of input length'}", []string{"Range of input length"}, true},
		{"empty list", "context_overflow: {patterns: []}", []string{}, true},
		{"invalid regexp", "context_overflow: {patterns: ['(unclosed']}", []string{"(unclosed"}, false},
		{"non-string element", "context_overflow: {patterns: [1]}", nil, false},
		{"object", "context_overflow: {patterns: {a: b}}", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := viper.New()
			v.SetConfigType("yaml")
			require.NoError(t, v.ReadConfig(strings.NewReader("agent_v3:\n  session:\n    "+tt.raw)))
			var cfg AgentV3Config
			require.NoError(t, v.UnmarshalKey("agent_v3", &cfg, viper.DecodeHook(DispatchFor())))
			err := cfg.Session.Validate()
			if !tt.valid {
				require.Error(t, err)
				if tt.name == "invalid regexp" {
					require.ErrorIs(t, err, errInvalidAgentV3SessionOverflowPatterns)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.Session.ContextOverflow.Patterns)
			require.EqualValues(t, 200000, cfg.Session.ContextOverflow.TokenLimit(), "patterns do not disturb the default limit")
		})
	}
}

func TestAgentV3SessionOverflowPatternsEnvironment(t *testing.T) {
	originalEnv, originalErr := runtimeEnvConfig, errRuntimeEnvConfigState
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(func() {
		runtimeEnvConfig, errRuntimeEnvConfigState = originalEnv, originalErr
		viper.Reset()
		restore()
	})
	viper.Reset()
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader("agent_v3: {}")))
	t.Setenv("SESSION_TEST_AGENT_V3_SESSION_CONTEXT_OVERFLOW_PATTERNS", "prompt is too long, try again")
	InitViper("", "SESSION_TEST")
	var cfg AgentV3Config
	cfg.readConfig()
	require.NoError(t, cfg.Session.Validate())
	require.Equal(t, []string{"prompt is too long, try again"}, cfg.Session.ContextOverflow.Patterns, "an environment value is one pattern; commas are not separators")
}
