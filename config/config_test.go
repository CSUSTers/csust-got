package config

import (
	"os"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

var (
	testConfigFile = "../config.yaml"
	testEnvPrefix  = "BOT_TEST"
)

func TestReadConfigFile(t *testing.T) {
	req := require.New(t)

	// init config
	BotConfig = NewBotConfig()
	initViper(testConfigFile, "")
	readConfig()
	defer viper.Reset()

	// some config should read
	req.False(BotConfig.DebugMode)
	req.Empty(BotConfig.Token)
	req.Equal("redis:6379", BotConfig.RedisConfig.RedisAddr)
	req.Equal("csust-bot-redis-password", BotConfig.RedisConfig.RedisPass)
}

func TestReadEnv(t *testing.T) {
	req := require.New(t)

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
}

func TestEnvOverrideFile(t *testing.T) {
	req := require.New(t)

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
	req := require.New(t)

	// init config
	BotConfig = NewBotConfig()
	initViper(testConfigFile, testEnvPrefix)
	readConfig()
	defer viper.Reset()

	config := BotConfig.RateLimitConfig
	req.Equal(20, config.MaxToken)
	req.Equal(1.0, config.Limit)
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
