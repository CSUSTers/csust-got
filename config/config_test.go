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
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
)

var (
	repoConfigFile = "../config.yaml"
	testEnvPrefix  = "BOT_TEST"
)

func testInit(t *testing.T) *require.Assertions {
	zap.ReplaceGlobals(zaptest.NewLogger(t, zaptest.WrapOptions(zap.AddCaller())))
	return require.New(t)
}

func isolatedConfigFile(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile(repoConfigFile)
	require.NoError(t, err)

	configFile := filepath.Join(t.TempDir(), "config.yaml")
	err = os.WriteFile(configFile, data, 0o644)
	require.NoError(t, err)

	return configFile
}

func TestReadConfigFile(t *testing.T) {
	req := testInit(t)
	configFile := isolatedConfigFile(t)

	// init config
	BotConfig = NewBotConfig()
	InitViper(configFile, "")
	readConfig()
	viper.Reset()

	// some config should read
	req.False(BotConfig.DebugMode)
	req.Empty(BotConfig.Token)
	req.Equal("redis:6379", BotConfig.RedisConfig.RedisAddr)
	req.Equal("csust-bot-redis-password", BotConfig.RedisConfig.RedisPass)
	// req.Equal("https://api.csu.st", BotConfig.GenShinConfig.ApiServer)
	// req.Equal("https://api.csu.st/file/VO_inGame/VO_NPC/NPC_DQ/vo_npc_dq_f_katheryne_01.ogg", BotConfig.GenShinConfig.ErrAudioAddr)

	InitViper("not_exist", "")
	readConfig()
	defer viper.Reset()

	// some config should empty
	req.False(BotConfig.DebugMode)
	req.Empty(BotConfig.Token)
	req.Empty(BotConfig.RedisConfig.RedisAddr)
	req.Empty(BotConfig.RedisConfig.RedisPass)
}

// nolint:goconst
func TestReadEnv(t *testing.T) {
	req := testInit(t)

	// set some env
	t.Setenv(testEnvPrefix+"_"+"DEBUG", "true")
	t.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	t.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")
	t.Setenv(testEnvPrefix+"_"+"REDIS_PASS", "some-env-password")

	// init config
	BotConfig = NewBotConfig()
	InitViper("", testEnvPrefix)
	readConfig()
	defer viper.Reset()

	// some config should read
	req.True(BotConfig.DebugMode)
	req.Equal("some-bot-token", BotConfig.Token)
	req.Equal("some-env-address", BotConfig.RedisConfig.RedisAddr)
	req.Equal("some-env-password", BotConfig.RedisConfig.RedisPass)
	checkConfig()
}

func TestEnvOverrideFile(t *testing.T) {
	req := testInit(t)
	configFile := isolatedConfigFile(t)

	// set some env
	t.Setenv(testEnvPrefix+"_"+"DEBUG", "true")
	t.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	t.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")

	// init config
	BotConfig = NewBotConfig()
	InitViper(configFile, testEnvPrefix)
	readConfig()
	defer viper.Reset()

	// some config should read
	req.True(BotConfig.DebugMode)
	req.Equal("some-bot-token", BotConfig.Token)
	req.Equal("some-env-address", BotConfig.RedisConfig.RedisAddr)
	req.Equal("csust-bot-redis-password", BotConfig.RedisConfig.RedisPass)
}

func TestMustConfig(t *testing.T) {
	testInit(t)
	mustConfigs := []string{"TOKEN", "REDIS_ADDR"}

	// set must config env
	for _, v := range mustConfigs {
		t.Setenv(testEnvPrefix+"_"+""+v, v)
	}

	// all set should not panic
	BotConfig = NewBotConfig()
	InitViper("", testEnvPrefix)
	readConfig()
	require.NotPanics(t, func() { checkConfig() })
	defer viper.Reset()

	// every missing request should panic
	errMsgs := []string{noTokenMsg, noRedisMsg}
	for i, v := range mustConfigs {
		_ = os.Unsetenv(testEnvPrefix + "_" + "" + v)                    // unset env
		readConfig()                                                     // read config
		require.PanicsWithValue(t, errMsgs[i], func() { checkConfig() }) // should panic
		t.Setenv(testEnvPrefix+"_"+""+v, v)                              // set env
	}
}

func TestRateLimitConfig(t *testing.T) {
	req := testInit(t)
	configFile := isolatedConfigFile(t)

	// init config
	BotConfig = NewBotConfig()
	InitViper(configFile, testEnvPrefix)
	readConfig()
	defer viper.Reset()

	config := BotConfig.RateLimitConfig
	req.Equal(20, config.MaxToken)
	req.Equal(0.5, config.Limit)
	req.Equal(1, config.Cost)
	req.Equal(3, config.StickerCost)
	req.Equal(2, config.CommandCost)

	// set some env
	t.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	t.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_MAX_TOKEN", "0")
	t.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_LIMIT", "0")
	t.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_COST", "-1")
	t.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_COST_STICKER", "-1")
	t.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_COST_COMMAND", "-1")

	// should override by env
	readConfig()

	config = BotConfig.RateLimitConfig
	req.Equal(0, config.MaxToken)
	req.Equal(0.0, config.Limit)
	req.Equal(-1, config.Cost)
	req.Equal(-1, config.StickerCost)
	req.Equal(-1, config.CommandCost)

	// should check to default
	checkConfig()
	req.Equal(1, config.MaxToken)
	req.Equal(1.0, config.Limit)
	req.Equal(1, config.Cost)
	req.Equal(1, config.StickerCost)
	req.Equal(1, config.CommandCost)
}

func TestMessageConfig(t *testing.T) {
	req := testInit(t)
	configFile := isolatedConfigFile(t)

	// set some env
	t.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	t.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")
	// init config
	BotConfig = NewBotConfig()
	InitViper(configFile, testEnvPrefix)
	readConfig()
	defer viper.Reset()

	req.Equal("好 的， 我 杀 我 自 己。", BotConfig.MessageConfig.RestrictBot)

	// set some env
	t.Setenv(testEnvPrefix+"_"+"MESSAGE_RESTRICT_BOT", "")
	readConfig()
	req.Equal("", BotConfig.MessageConfig.RestrictBot)

	checkConfig()
	req.Equal(missMsg, BotConfig.MessageConfig.RestrictBot)
}

func TestSpecialListConfig(t *testing.T) {
	req := testInit(t)
	configFile := isolatedConfigFile(t)

	// set some env
	t.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	t.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")

	// init config
	BotConfig = NewBotConfig()

	InitViper(configFile, testEnvPrefix)
	readConfig()

	defer viper.Reset()

	req.True(BotConfig.BlockListConfig.Enabled)
	req.True(BotConfig.WhiteListConfig.Enabled)
}

func TestAgentConfigs(t *testing.T) {
	req := testInit(t)
	configFile := isolatedConfigFile(t)

	// init config
	BotConfig = NewBotConfig()

	InitViper(configFile, testEnvPrefix)
	readConfig()

	defer viper.Reset()

	req.Greater(len(*BotConfig.Agents), 0)
	req.NotNil((*BotConfig.Agents)[0].Model)
	req.NotEmpty((*BotConfig.Agents)[0].Model.Model)
	req.NotNil((*BotConfig.Agents)[0].Agent)
	req.True((*BotConfig.Agents)[0].Agent.Enable)
}

// TestExampleConfigYAMLAgentsParses keeps the tracked example config.yaml in
// sync with the config structs: model anchors must expand, inline filters must
// parse, context_mode stays at its chat default, and known-dead top-level keys
// must not creep back into the example.
func TestExampleConfigYAMLAgentsParses(t *testing.T) {
	req := testInit(t)
	configFile := isolatedConfigFile(t)

	BotConfig = NewBotConfig()
	InitViper(configFile, testEnvPrefix)
	readConfig()
	defer viper.Reset()

	agents := *BotConfig.Agents
	req.Len(agents, 4)

	seen := make(map[string]bool, len(agents))
	for _, agent := range agents {
		req.NotEmpty(agent.Name)
		seen[agent.Name] = true
		req.NotNil(agent.Model)
		req.NotEmpty(agent.Model.Model)
		req.NotEmpty(agent.Model.BaseUrl)
		req.NoError(agent.ValidateContextMode())
		req.Empty(agent.ContextMode)
	}
	req.True(seen["什么是bot"])
	req.True(seen["聊天bot"])
	req.True(seen["思考bot"])
	req.True(seen["总结bot"])

	var think *AgentConfig
	for _, agent := range agents {
		if agent.Name == "思考bot" {
			think = agent
		}
	}
	req.Len(think.Filters.Filters, 1)
	req.Equal("whitelist", think.Filters.Filters[0].Type)

	for _, deadKey := range []string{"worker", "llm_models", "chat_whitelist", "github", "mc.max_count"} {
		req.False(viper.IsSet(deadKey), "dead config key %q must stay out of the example config", deadKey)
	}
}

func TestConfigYAMLKeepsFetchDisabledByDefault(t *testing.T) {
	req := testInit(t)
	configFile := isolatedConfigFile(t)

	BotConfig = NewBotConfig()
	InitViper(configFile, testEnvPrefix)
	readConfig()
	BotConfig.AgentV3.checkConfig()
	defer viper.Reset()

	req.NotNil(BotConfig.AgentV3)
	req.True(BotConfig.AgentV3.Enable)
	req.Equal("docs/agent_v3_soul.md", BotConfig.AgentV3.SoulPath)
	req.True(BotConfig.AgentV3.ContextCache.Enable)
	req.Equal(12, BotConfig.AgentV3.ContextCache.RawTurns)
	req.Equal("http://agent-runtime:8080", BotConfig.AgentV3.Runtime.Endpoint)
	req.NotNil(BotConfig.AgentV3.Runtime.FetchEnabled)
	req.False(BotConfig.AgentV3.RuntimeFetchEnabled())
	req.Equal([]string{"read", "grep", "write", "edit", "bash"}, BotConfig.AgentV3.Tools.ExposeOnly)
	req.Equal(30*24*time.Hour, BotConfig.AgentV3.ContextCacheTTL())
	req.Equal(120*time.Second, BotConfig.AgentV3.RuntimeCommandTimeout())
	req.Empty(BotConfig.AgentV3.Skills.Root)
	req.False(BotConfig.AgentV3.Skills.RuntimeGlobal)
	req.False(BotConfig.AgentV3.Skills.SearXNG.Enable)
	req.Equal("https://search.example.org", BotConfig.AgentV3.Skills.SearXNG.BaseURL)
	req.Equal("SEARXNG_USERNAME", BotConfig.AgentV3.Skills.SearXNG.UsernameEnv)
	req.Equal("SEARXNG_PASSWORD", BotConfig.AgentV3.Skills.SearXNG.PasswordEnv)
	req.Equal(10*time.Second, BotConfig.AgentV3.SearXNGTimeout())
}

func setupCustomConfigTest(t *testing.T) (*require.Assertions, *observer.ObservedLogs, string) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	logger := zap.L()
	core, logs := observer.New(zap.WarnLevel)
	zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(func() { zap.ReplaceGlobals(logger) })

	return require.New(t), logs, filepath.Join(t.TempDir(), "config.yaml")
}

func writeCustomConfigBase(t *testing.T, configFile string) {
	t.Helper()
	require.NoError(t, os.WriteFile(configFile, []byte("debug: false\ntoken: base-token\n"), 0o644))
}

func customConfigWarnings(logs *observer.ObservedLogs) []observer.LoggedEntry {
	return logs.FilterMessage("an error was produced when reading custom config!").All()
}

func requireCustomConfigWarning(t *testing.T, logs *observer.ObservedLogs, customConfigFile string) {
	t.Helper()

	warnings := customConfigWarnings(logs)
	require.Len(t, warnings, 1)
	require.Equal(t, customConfigFile, warnings[0].ContextMap()["customConfigFile"])
	errorValue, ok := warnings[0].ContextMap()["error"]
	require.True(t, ok)
	require.NotEmpty(t, errorValue)

	hasErrorField := false
	for _, field := range warnings[0].Context {
		if field.Key == "error" && field.Type == zapcore.ErrorType {
			hasErrorField = true
			break
		}
	}
	require.True(t, hasErrorField)
}

func TestCustomConfigMissingIsSilent(t *testing.T) {
	req, logs, configFile := setupCustomConfigTest(t)
	writeCustomConfigBase(t, configFile)

	InitViper(configFile, "")

	req.Empty(customConfigWarnings(logs))
	req.False(viper.GetBool("debug"))
	req.Equal("base-token", viper.GetString("token"))
}

func TestCustomConfigMalformedLogsDiagnostic(t *testing.T) {
	req, logs, configFile := setupCustomConfigTest(t)
	writeCustomConfigBase(t, configFile)
	customConfigFile := filepath.Join(filepath.Dir(configFile), "custom.yaml")
	req.NoError(os.WriteFile(customConfigFile, []byte("debug: [unterminated"), 0o644))

	InitViper(configFile, "")

	requireCustomConfigWarning(t, logs, customConfigFile)
}

func TestCustomConfigUnreadableLogsDiagnostic(t *testing.T) {
	req, logs, configFile := setupCustomConfigTest(t)
	writeCustomConfigBase(t, configFile)
	customConfigFile := filepath.Join(filepath.Dir(configFile), "custom.yaml")
	req.NoError(os.Mkdir(customConfigFile, 0o755))

	InitViper(configFile, "")

	requireCustomConfigWarning(t, logs, customConfigFile)
}

func TestCustomConfigValidOverridesBase(t *testing.T) {
	req, logs, configFile := setupCustomConfigTest(t)
	writeCustomConfigBase(t, configFile)
	customConfigFile := filepath.Join(filepath.Dir(configFile), "custom.yaml")
	req.NoError(os.WriteFile(customConfigFile, []byte("debug: true\ntoken: custom-token\n"), 0o644))

	BotConfig = NewBotConfig()
	InitViper(configFile, "")
	readConfig()

	req.Empty(customConfigWarnings(logs))
	req.True(BotConfig.DebugMode)
	req.Equal("custom-token", BotConfig.Token)
}

func TestRuntimeEnvPreservesNamesAndLiteralValues(t *testing.T) {
	_, _, configFile := setupCustomConfigTest(t)
	require.NoError(t, os.WriteFile(configFile, []byte(`token: test-token
redis:
  addr: localhost:6379
agent_v3:
  runtime:
    env:
      ApiToken: "${NOT_EXPANDED}"
      EMPTY: ""
      URL: "https://example.org/?a=1&b=2"
`), 0o644))
	t.Setenv("BOT_AGENT_V3_RUNTIME_ENV_UNDECLARED", "not imported")
	BotConfig = NewBotConfig()
	InitViper(configFile, "BOT")
	readConfig()
	require.Equal(t, map[string]string{
		"ApiToken": "${NOT_EXPANDED}",
		"EMPTY":    "",
		"URL":      "https://example.org/?a=1&b=2",
	}, BotConfig.AgentV3.Runtime.Env)
	require.NotPanics(t, checkConfig)
}

func TestRuntimeEnvCustomAndDeclaredEnvOverrides(t *testing.T) {
	_, _, configFile := setupCustomConfigTest(t)
	require.NoError(t, os.WriteFile(configFile, []byte(`agent_v3:
  runtime:
    env:
      ApiToken: "base"
      SHARED: "base"
      BASE_ONLY: "base"
`), 0o644))
	customFile := filepath.Join(filepath.Dir(configFile), "custom.yaml")
	require.NoError(t, os.WriteFile(customFile, []byte(`agent_v3:
  runtime:
    env:
      ApiToken: "custom"
      SHARED: ""
      CUSTOM_ONLY: "custom"
`), 0o644))
	t.Setenv("BOT_AGENT_V3_RUNTIME_ENV_APITOKEN", "")
	t.Setenv("BOT_AGENT_V3_RUNTIME_ENV_SHARED", "host override")
	t.Setenv("BOT_AGENT_V3_RUNTIME_ENV_UNDECLARED", "not imported")
	BotConfig = NewBotConfig()
	InitViper(configFile, "BOT")
	ReadConfig(BotConfig.AgentV3)
	require.Equal(t, map[string]string{
		"ApiToken":    "",
		"SHARED":      "host override",
		"BASE_ONLY":   "base",
		"CUSTOM_ONLY": "custom",
	}, BotConfig.AgentV3.Runtime.Env)
	require.NoError(t, ValidateAgentV3RuntimeEnv(BotConfig.AgentV3.Runtime.Env))
}

func TestRuntimeEnvMissingIgnoresHostValues(t *testing.T) {
	_, _, configFile := setupCustomConfigTest(t)
	writeCustomConfigBase(t, configFile)
	t.Setenv("BOT_AGENT_V3_RUNTIME_ENV_SECRET", "host-only")
	t.Setenv("BOT_AGENT_V3_RUNTIME_ENV", `{"SECRET":"host-only"}`)
	BotConfig = NewBotConfig()
	InitViper(configFile, "BOT")
	readConfig()
	require.Empty(t, BotConfig.AgentV3.Runtime.Env)
}

func TestRuntimeEnvWholeMapEnvDoesNotOverrideDeclaredKeys(t *testing.T) {
	_, _, configFile := setupCustomConfigTest(t)
	require.NoError(t, os.WriteFile(configFile, []byte("agent_v3:\n  enable: true\n  runtime:\n    env: {KEY: base}\n"), 0o644))
	t.Setenv("BOT_AGENT_V3_RUNTIME_ENV", `{"KEY":"host"}`)
	BotConfig = NewBotConfig()
	InitViper(configFile, "BOT")
	readConfig()
	require.True(t, BotConfig.AgentV3.Enable)
	require.Equal(t, map[string]string{"KEY": "base"}, BotConfig.AgentV3.Runtime.Env)
}

func TestRuntimeEnvInvalidInputFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		env  string
	}{
		{"null", "null"},
		{"number", "{KEY: 123}"},
		{"boolean", "{KEY: true}"},
		{"sequence", "[one, two]"},
		{"nested map", "{KEY: {nested: value}}"},
		{"duplicate", "{KEY: first, KEY: second}"},
		{"invalid name", "{1KEY: secret}"},
		{"reserved name", "{PATH: secret}"},
		{"lookup collision", "{ApiKey: secret, APIKEY: different}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, configFile := setupCustomConfigTest(t)
			require.NoError(t, os.WriteFile(configFile, []byte("token: test-token\nredis:\n  addr: localhost:6379\nagent_v3:\n  runtime:\n    env: "+tt.env+"\n"), 0o644))
			BotConfig = NewBotConfig()
			InitViper(configFile, "BOT")
			readConfig()
			require.Panics(t, checkConfig)
			require.PanicsWithValue(t, "invalid agent_v3 runtime env", func() { InitConfig(configFile, "BOT") })
		})
	}
}

func TestRuntimeEnvInvalidCustomDoesNotFallbackToBase(t *testing.T) {
	_, _, configFile := setupCustomConfigTest(t)
	require.NoError(t, os.WriteFile(configFile, []byte("token: test-token\nredis:\n  addr: localhost:6379\nagent_v3:\n  runtime:\n    env: {KEY: base}\n"), 0o644))
	customFile := filepath.Join(filepath.Dir(configFile), "custom.yaml")
	require.NoError(t, os.WriteFile(customFile, []byte("agent_v3:\n  runtime:\n    env: {KEY: null}\n"), 0o644))
	require.Panics(t, func() { InitConfig(configFile, "BOT") })
}

func TestRuntimeEnvLookupCollisionAcrossFiles(t *testing.T) {
	_, _, configFile := setupCustomConfigTest(t)
	require.NoError(t, os.WriteFile(configFile, []byte("agent_v3:\n  runtime:\n    env: {ApiKey: base}\n"), 0o644))
	customFile := filepath.Join(filepath.Dir(configFile), "custom.yaml")
	require.NoError(t, os.WriteFile(customFile, []byte("agent_v3:\n  runtime:\n    env: {APIKEY: custom}\n"), 0o644))
	BotConfig = NewBotConfig()
	InitViper(configFile, "BOT")
	readConfig()
	require.ErrorContains(t, errRuntimeEnvConfigState, "runtime_env_lookup_collision")
	require.PanicsWithValue(t, "invalid agent_v3 runtime env", checkConfig)
}

func TestRuntimeEnvDeclaredHostOverrideIsValidatedAtStartup(t *testing.T) {
	_, _, configFile := setupCustomConfigTest(t)
	require.NoError(t, os.WriteFile(configFile, []byte("token: test-token\nredis:\n  addr: localhost:6379\nagent_v3:\n  runtime:\n    env: {KEY: base}\n"), 0o644))
	t.Setenv("BOT_AGENT_V3_RUNTIME_ENV_KEY", strings.Repeat("x", 2049))
	require.PanicsWithValue(t, "invalid agent_v3 runtime env", func() { InitConfig(configFile, "BOT") })
}

func TestRuntimeEnvRejectsAliasedRuntimeSection(t *testing.T) {
	_, err := parseRuntimeEnvYAML([]byte("shared: &settings {env: {PRIVATE: synthetic-secret}}\nagent_v3:\n  runtime: *settings\n"))
	require.ErrorContains(t, err, "runtime_env_type")
	require.NotContains(t, err.Error(), "synthetic-secret")
}

func TestRuntimeEnvInheritedYAMLMergeFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{"root", "defaults: &cfg {agent_v3: {runtime: {env: {SECRET: synthetic-secret}}}}\n<<: *cfg\n"},
		{"agent_v3", "agent_v3:\n  defaults: &cfg {runtime: {env: {SECRET: synthetic-secret}}}\n  <<: *cfg\n"},
		{"runtime", "agent_v3:\n  runtime:\n    defaults: &cfg {env: {SECRET: synthetic-secret}}\n    <<: *cfg\n"},
		{"env", "agent_v3:\n  runtime:\n    defaults: &cfg {SECRET: synthetic-secret}\n    env:\n      <<: *cfg\n      DIRECT: literal\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, configFile := setupCustomConfigTest(t)
			require.NoError(t, os.WriteFile(configFile, []byte("token: test-token\nredis:\n  addr: localhost:6379\n"+tt.body), 0o644))
			BotConfig = NewBotConfig()
			InitViper(configFile, "BOT")
			ReadConfig(BotConfig.AgentV3)
			require.Equal(t, "synthetic-secret", viper.GetString("agent_v3.runtime.env.secret"), "Viper sees an inherited env value")
			require.ErrorContains(t, errRuntimeEnvConfigState, "runtime_env_merge")
			require.NotContains(t, errRuntimeEnvConfigState.Error(), "synthetic-secret")
			require.PanicsWithValue(t, "invalid agent_v3 runtime env", checkConfig)
		})
	}
}

func TestRuntimeEnvExplicitConfigurationAfterUnrelatedMerge(t *testing.T) {
	_, _, configFile := setupCustomConfigTest(t)
	require.NoError(t, os.WriteFile(configFile, []byte("token: test-token\nredis:\n  addr: localhost:6379\nmisc:\n  defaults: &other {debug: true}\n  <<: *other\nagent_v3:\n  runtime:\n    env: {DIRECT: literal}\n"), 0o644))
	BotConfig = NewBotConfig()
	InitViper(configFile, "BOT")
	ReadConfig(BotConfig.AgentV3)
	require.NoError(t, errRuntimeEnvConfigState)
	require.Equal(t, map[string]string{"DIRECT": "literal"}, BotConfig.AgentV3.Runtime.Env)
}

func TestRuntimeEnvCustomInheritedMergeDoesNotFallBackToBase(t *testing.T) {
	_, _, configFile := setupCustomConfigTest(t)
	require.NoError(t, os.WriteFile(configFile, []byte("token: test-token\nredis:\n  addr: localhost:6379\nagent_v3:\n  runtime:\n    env: {BASE: literal}\n"), 0o644))
	customFile := filepath.Join(filepath.Dir(configFile), "custom.yaml")
	require.NoError(t, os.WriteFile(customFile, []byte("agent_v3:\n  runtime:\n    defaults: &cfg {env: {CUSTOM: synthetic-secret}}\n    <<: *cfg\n"), 0o644))
	BotConfig = NewBotConfig()
	InitViper(configFile, "BOT")
	ReadConfig(BotConfig.AgentV3)
	require.ErrorContains(t, errRuntimeEnvConfigState, "runtime_env_merge")
	require.NotContains(t, errRuntimeEnvConfigState.Error(), "synthetic-secret")
	require.PanicsWithValue(t, "invalid agent_v3 runtime env", checkConfig)
}
