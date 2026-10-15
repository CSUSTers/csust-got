package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAgentSessionSettingsYAML(t *testing.T) {
	tests := []struct {
		name        string
		session     string
		defaultSave bool
		save        bool
		load        bool
	}{
		{name: "omitted", defaultSave: true, save: true},
		{name: "empty", session: "    session: {}\n", defaultSave: true, save: true},
		{name: "explicit false", session: "    session:\n      save_context: false\n      load_context: false\n"},
		{name: "save only", session: "    session:\n      save_context: true\n      load_context: false\n", save: true},
		{name: "load only", session: "    session:\n      save_context: false\n      load_context: true\n", load: true},
		{name: "save and load", session: "    session:\n      save_context: true\n      load_context: true\n", save: true, load: true},
		{name: "load with default save", session: "    session:\n      load_context: true\n", defaultSave: true, save: true, load: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := viper.New()
			v.SetConfigType("yaml")
			require.NoError(t, v.ReadConfig(strings.NewReader("agents:\n  - name: example\n"+tt.session)))
			var agents AgentV3Configs
			require.NoError(t, v.UnmarshalKey("agents", &agents, viper.DecodeHook(DispatchFor())))
			require.Len(t, agents, 1)
			cfg := agents[0]
			if tt.defaultSave {
				require.Nil(t, cfg.Session.SaveContext)
			} else {
				require.NotNil(t, cfg.Session.SaveContext)
				require.Equal(t, tt.save, *cfg.Session.SaveContext)
			}
			require.Equal(t, tt.save, cfg.Session.SaveEnabled())
			require.Equal(t, tt.load, cfg.Session.LoadEnabled())

			for _, trigger := range []*AgentTrigger{nil, {Command: "chat"}, {Regex: ".*"}} {
				save, load := cfg.EffectiveSessionSettings(trigger)
				require.Equal(t, tt.save, save)
				require.Equal(t, tt.load, load)
			}
			original := cfg.Session
			save, load := cfg.EffectiveSessionSettings(&AgentTrigger{Reply: true})
			require.True(t, save)
			require.True(t, load)
			require.Equal(t, original, cfg.Session)
			require.Equal(t, tt.save, cfg.Session.SaveEnabled())
			require.Equal(t, tt.load, cfg.Session.LoadEnabled())
		})
	}
}

func TestAgentSessionZeroValue(t *testing.T) {
	var session AgentSessionConfig
	require.True(t, session.SaveEnabled())
	require.False(t, session.LoadEnabled())
	var cfg *AgentConfig
	save, load := cfg.EffectiveSessionSettings(nil)
	require.True(t, save)
	require.False(t, load)
	save, load = cfg.EffectiveSessionSettings(&AgentTrigger{Reply: true})
	require.True(t, save)
	require.True(t, load)
}

func TestAgentV3SessionYAML(t *testing.T) {
	restoreLogger := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(restoreLogger)
	tests := []struct {
		name    string
		raw     string
		ttl     time.Duration
		invalid bool
	}{
		{name: "omitted", raw: "agent_v3: {}", ttl: 24 * time.Hour},
		{name: "empty session", raw: "agent_v3:\n  session: {}", ttl: 24 * time.Hour},
		{name: "empty ttl defaults", raw: "agent_v3:\n  session:\n    ttl: ''", ttl: 24 * time.Hour},
		{name: "override", raw: "agent_v3:\n  session:\n    ttl: 36h", ttl: 36 * time.Hour},
		{name: "compound duration", raw: "agent_v3:\n  session:\n    ttl: 1h30m", ttl: 90 * time.Minute},
		{name: "minimum positive duration", raw: "agent_v3:\n  session:\n    ttl: 1ns", ttl: time.Nanosecond},
		{name: "malformed", raw: "agent_v3:\n  session:\n    ttl: tomorrow", invalid: true},
		{name: "days unsupported", raw: "agent_v3:\n  session:\n    ttl: 1d", invalid: true},
		{name: "whitespace", raw: "agent_v3:\n  session:\n    ttl: ' '", invalid: true},
		{name: "zero", raw: "agent_v3:\n  session:\n    ttl: 0s", invalid: true},
		{name: "numeric zero", raw: "agent_v3:\n  session:\n    ttl: 0", invalid: true},
		{name: "negative", raw: "agent_v3:\n  session:\n    ttl: -24h", invalid: true},
		{name: "numeric negative", raw: "agent_v3:\n  session:\n    ttl: -1", invalid: true},
		{name: "overflow", raw: "agent_v3:\n  session:\n    ttl: 999999999999h", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := viper.New()
			v.SetConfigType("yaml")
			require.NoError(t, v.ReadConfig(strings.NewReader(tt.raw)))
			var cfg AgentV3Config
			require.NoError(t, v.UnmarshalKey("agent_v3", &cfg, viper.DecodeHook(DispatchFor())))
			originalTTL := cfg.Session.TTL
			if tt.invalid {
				require.ErrorIs(t, cfg.Session.Validate(), errInvalidAgentV3SessionTTL)
				require.PanicsWithValue(t, "invalid agent_v3 session config", cfg.checkConfig)
				require.Equal(t, originalTTL, cfg.Session.TTL)
				return
			}
			require.NoError(t, cfg.Session.Validate())
			require.Equal(t, "data/agent-sessions", cfg.Session.DirectoryPath())
			require.Equal(t, tt.ttl, cfg.Session.IdleTTL())
			cfg.checkConfig()
			require.Equal(t, "data/agent-sessions", cfg.Session.Directory)
			require.NotEmpty(t, cfg.Session.TTL)
			require.Equal(t, tt.ttl, cfg.Session.IdleTTL())
			if originalTTL != "" {
				require.Equal(t, originalTTL, cfg.Session.TTL)
			}
		})
	}
}

func TestAgentSessionInitConfig(t *testing.T) {
	originalConfig := BotConfig
	originalEnv, originalEnvError := runtimeEnvConfig, errRuntimeEnvConfigState
	restoreLogger := zap.ReplaceGlobals(zap.NewNop())
	viper.Reset()
	t.Cleanup(func() {
		BotConfig = originalConfig
		runtimeEnvConfig, errRuntimeEnvConfigState = originalEnv, originalEnvError
		viper.Reset()
		restoreLogger()
	})
	for _, ttl := range []string{"", "ttl: 48h", "ttl: invalid", "ttl: 0s", "ttl: -1h", "ttl: 0", "ttl: false", "ttl: []", "ttl: [24h]", "ttl: {}", "ttl: {hours: 24}"} {
		t.Run(ttl, func(t *testing.T) {
			viper.Reset()
			raw := `
token: test-token
redis:
  addr: localhost:6379
agent_v3:
  enable: false
  session:
    directory: custom/sessions
    ` + ttl + `
agents:
  - name: example
    session:
      save_context: false
      load_context: false
`
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
			if ttl != "" && ttl != "ttl: 48h" {
				require.PanicsWithValue(t, "invalid agent_v3 session config", func() { InitConfig(path, "SESSION_TEST") })
				return
			}
			require.NotPanics(t, func() { InitConfig(path, "SESSION_TEST") })
			require.Equal(t, "custom/sessions", BotConfig.AgentV3.Session.Directory)
			require.Equal(t, "custom/sessions", BotConfig.AgentV3.Session.DirectoryPath())
			wantTTL := 24 * time.Hour
			if ttl != "" {
				wantTTL = 48 * time.Hour
			}
			require.Equal(t, wantTTL, BotConfig.AgentV3.Session.IdleTTL())
			require.Len(t, *BotConfig.Agents, 1)
			cfg := (*BotConfig.Agents)[0]
			require.NotNil(t, cfg.Session.SaveContext)
			require.False(t, cfg.Session.SaveEnabled())
			require.False(t, cfg.Session.LoadEnabled())
		})
	}
}

func TestAgentSessionTTLFromViperEnvWithoutYAMLKey(t *testing.T) {
	originalEnv, originalEnvError := runtimeEnvConfig, errRuntimeEnvConfigState
	restoreLogger := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(func() {
		runtimeEnvConfig, errRuntimeEnvConfigState = originalEnv, originalEnvError
		viper.Reset()
		restoreLogger()
	})
	for _, ttl := range []string{"36h", "invalid", "0s", "-1h"} {
		t.Run(ttl, func(t *testing.T) {
			viper.Reset()
			viper.SetConfigType("yaml")
			require.NoError(t, viper.ReadConfig(strings.NewReader("agent_v3: {}")))
			t.Setenv("SESSION_TEST_AGENT_V3_SESSION_TTL", ttl)
			InitViper("", "SESSION_TEST")
			var cfg AgentV3Config
			cfg.readConfig()
			require.Equal(t, ttl, cfg.Session.TTL)
			if ttl == "36h" {
				require.NotPanics(t, cfg.checkConfig)
				require.Equal(t, 36*time.Hour, cfg.Session.IdleTTL())
				return
			}
			require.PanicsWithValue(t, "invalid agent_v3 session config", cfg.checkConfig)
		})
	}
}
