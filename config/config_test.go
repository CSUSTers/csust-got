package config

import (
	"os"
	"path/filepath"
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
