package config

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"os"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

var (
	testConfigFile = "../config.yaml"
	testEnvPrefix  = "BOT_TEST"
)

func testInit(t *testing.T) *require.Assertions {
	zap.ReplaceGlobals(zaptest.NewLogger(t, zaptest.WrapOptions(zap.AddCaller())))
	return require.New(t)
}

func TestReadConfigFile(t *testing.T) {
	req := testInit(t)

	// init config
	BotConfig = NewBotConfig()
	initViper(testConfigFile, "")
	readConfig()
	viper.Reset()

	// some config should read
	req.False(BotConfig.DebugMode)
	req.Empty(BotConfig.Token)
	req.Equal("redis:6379", BotConfig.RedisConfig.RedisAddr)
	req.Equal("csust-bot-redis-password", BotConfig.RedisConfig.RedisPass)

	initViper("not_exist", "")
	readConfig()
	defer viper.Reset()

	// some config should empty
	req.False(BotConfig.DebugMode)
	req.Empty(BotConfig.Token)
	req.Empty(BotConfig.RedisConfig.RedisAddr)
	req.Empty(BotConfig.RedisConfig.RedisPass)
}

func TestReadEnv(t *testing.T) {
	req := testInit(t)

	// set some env
	_ = os.Setenv(testEnvPrefix+"_"+"DEBUG", "true")
	_ = os.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	_ = os.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")
	_ = os.Setenv(testEnvPrefix+"_"+"REDIS_PASS", "some-env-password")
	defer func() {
		_ = os.Unsetenv(testEnvPrefix + "_" + "DEBUG")
		_ = os.Unsetenv(testEnvPrefix + "_" + "TOKEN")
		_ = os.Unsetenv(testEnvPrefix + "_" + "REDIS_ADDR")
		_ = os.Unsetenv(testEnvPrefix + "_" + "REDIS_PASS")
	}()

	// init config
	BotConfig = NewBotConfig()
	initViper("", testEnvPrefix)
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

	// set some env
	_ = os.Setenv(testEnvPrefix+"_"+"DEBUG", "true")
	_ = os.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	_ = os.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")
	defer func() {
		_ = os.Unsetenv(testEnvPrefix + "_" + "DEBUG")
		_ = os.Unsetenv(testEnvPrefix + "_" + "TOKEN")
		_ = os.Unsetenv(testEnvPrefix + "_" + "REDIS_ADDR")
	}()

	// init config
	BotConfig = NewBotConfig()
	initViper(testConfigFile, testEnvPrefix)
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
		_ = os.Setenv(testEnvPrefix+"_"+""+v, v)
	}
	defer func() {
		for _, v := range mustConfigs {
			_ = os.Unsetenv(testEnvPrefix + "_" + "" + v)
		}
	}()

	// all set should not panic
	BotConfig = NewBotConfig()
	initViper("", testEnvPrefix)
	readConfig()
	require.NotPanics(t, func() { checkConfig() })
	defer viper.Reset()

	// every missing request should panic
	errMsgs := []string{noTokenMsg, noRedisMsg}
	for i, v := range mustConfigs {
		t.Run(v, func(t *testing.T) {
			_ = os.Unsetenv(testEnvPrefix + "_" + "" + v)                    // unset env
			readConfig()                                                     // read config
			require.PanicsWithValue(t, errMsgs[i], func() { checkConfig() }) // should panic
			_ = os.Setenv(testEnvPrefix+"_"+""+v, v)                         // set env
		})
	}
}

func TestRateLimitConfig(t *testing.T) {
	req := testInit(t)

	// init config
	BotConfig = NewBotConfig()
	initViper(testConfigFile, testEnvPrefix)
	readConfig()
	defer viper.Reset()

	config := BotConfig.RateLimitConfig
	req.Equal(20, config.MaxToken)
	req.Equal(0.5, config.Limit)
	req.Equal(1, config.Cost)
	req.Equal(3, config.StickerCost)
	req.Equal(2, config.CommandCost)

	// set some env
	_ = os.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	_ = os.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_MAX_TOKEN", "0")
	_ = os.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_LIMIT", "0")
	_ = os.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_COST", "-1")
	_ = os.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_COST_STICKER", "-1")
	_ = os.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_COST_COMMAND", "-1")
	defer func() {
		_ = os.Unsetenv(testEnvPrefix + "_" + "TOKEN")
		_ = os.Unsetenv(testEnvPrefix + "_" + "RATE_LIMIT_MAX_TOKEN")
		_ = os.Unsetenv(testEnvPrefix + "_" + "RATE_LIMIT_LIMIT")
		_ = os.Unsetenv(testEnvPrefix + "_" + "RATE_LIMIT_COST")
		_ = os.Unsetenv(testEnvPrefix + "_" + "RATE_LIMIT_COST_STICKER")
		_ = os.Unsetenv(testEnvPrefix + "_" + "RATE_LIMIT_COST_COMMAND")
	}()

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

	// set some env
	_ = os.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	_ = os.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")
	defer func() {
		_ = os.Unsetenv(testEnvPrefix + "_" + "TOKEN")
		_ = os.Unsetenv(testEnvPrefix + "_" + "REDIS_ADDR")
	}()

	// init config
	BotConfig = NewBotConfig()
	initViper(testConfigFile, testEnvPrefix)
	readConfig()
	defer viper.Reset()

	req.Equal("好 的， 我 杀 我 自 己。", BotConfig.MessageConfig.RestrictBot)

	// set some env
	_ = os.Setenv(testEnvPrefix+"_"+"MESSAGE_RESTRICT_BOT", "")
	defer func() {
		_ = os.Unsetenv(testEnvPrefix + "_" + "MESSAGE_RESTRICT_BOT")
	}()
	readConfig()
	req.Equal("", BotConfig.MessageConfig.RestrictBot)

	checkConfig()
	req.Equal(missMsg, BotConfig.MessageConfig.RestrictBot)
}

func TestSpecialListConfig(t *testing.T) {
	req := testInit(t)

	// set some env
	_ = os.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	_ = os.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")
	defer func() {
		_ = os.Unsetenv(testEnvPrefix + "_" + "TOKEN")
		_ = os.Unsetenv(testEnvPrefix + "_" + "REDIS_ADDR")
	}()

	// init config
	BotConfig = NewBotConfig()
	initViper(testConfigFile, testEnvPrefix)
	readConfig()
	defer viper.Reset()

	req.True(BotConfig.BlackListConfig.Enabled)
	req.True(BotConfig.WhiteListConfig.Enabled)
}
