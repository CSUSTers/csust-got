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
	req.Equal("redis:6379", BotConfig.RedisAddr)
	req.Equal("csust-bot-redis-password", BotConfig.RedisPass)
}

func TestReadEnv(t *testing.T) {
	req := require.New(t)

	// set some env
	os.Setenv(testEnvPrefix+"_"+"DEBUG", "true")
	os.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	os.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")
	os.Setenv(testEnvPrefix+"_"+"REDIS_PASS", "some-env-password")
	defer func() {
		os.Unsetenv(testEnvPrefix + "_" + "DEBUG")
		os.Unsetenv(testEnvPrefix + "_" + "TOKEN")
		os.Unsetenv(testEnvPrefix + "_" + "REDIS_ADDR")
		os.Unsetenv(testEnvPrefix + "_" + "REDIS_PASS")
	}()

	// init config
	BotConfig = NewBotConfig()
	initViper("", testEnvPrefix)
	readConfig()
	defer viper.Reset()

	// some config should read
	//req.True(BotConfig.DebugMode)
	req.Equal("some-bot-token", BotConfig.Token)
	req.Equal("some-env-address", BotConfig.RedisAddr)
	req.Equal("some-env-password", BotConfig.RedisPass)
}

func TestEnvOverrideFile(t *testing.T) {
	req := require.New(t)

	// set some env
	os.Setenv(testEnvPrefix+"_"+"DEBUG", "true")
	os.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	os.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")
	defer func() {
		os.Unsetenv(testEnvPrefix + "_" + "DEBUG")
		os.Unsetenv(testEnvPrefix + "_" + "TOKEN")
		os.Unsetenv(testEnvPrefix + "_" + "REDIS_ADDR")
	}()

	// init config
	BotConfig = NewBotConfig()
	initViper(testConfigFile, testEnvPrefix)
	readConfig()
	defer viper.Reset()

	// some config should read
	req.True(BotConfig.DebugMode)
	req.Equal("some-bot-token", BotConfig.Token)
	req.Equal("some-env-address", BotConfig.RedisAddr)
	req.Equal("csust-bot-redis-password", BotConfig.RedisPass)
}

func TestMustConfig(t *testing.T) {
	mustConfigs := []string{"TOKEN", "REDIS_ADDR"}

	// set must config env
	for _, v := range mustConfigs {
		os.Setenv(testEnvPrefix+"_"+""+v, v)
	}
	defer func() {
		for _, v := range mustConfigs {
			os.Unsetenv(testEnvPrefix + "_" + "" + v)
		}
	}()

	// all set should not panic
	BotConfig = NewBotConfig()
	initViper("", testEnvPrefix)
	readConfig()
	require.NotPanics(t, func() { checkConfig() })
	viper.Reset()

	// every missing request should panic
	errMsgs := []string{noTokenMsg, noRedisMsg}
	for i, v := range mustConfigs {
		t.Run(v, func(t *testing.T) {
			os.Unsetenv(testEnvPrefix + "_" + "" + v)
			// init config
			BotConfig = NewBotConfig()
			initViper("", testEnvPrefix)
			readConfig()
			defer viper.Reset()

			require.PanicsWithValue(t, errMsgs[i], func() { checkConfig() })
			os.Setenv(testEnvPrefix+"_"+""+v, v)
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

	// set some env
	os.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	os.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_MAX_TOKEN", "0")
	os.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_LIMIT", "0")
	os.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_COST", "-1")
	os.Setenv(testEnvPrefix+"_"+"RATE_LIMIT_COST_STICKER", "-1")
	defer func() {
		os.Unsetenv(testEnvPrefix + "_" + "TOKEN")
		os.Unsetenv(testEnvPrefix + "_" + "RATE_LIMIT_MAX_TOKEN")
		os.Unsetenv(testEnvPrefix + "_" + "RATE_LIMIT_LIMIT")
		os.Unsetenv(testEnvPrefix + "_" + "RATE_LIMIT_COST")
		os.Unsetenv(testEnvPrefix + "_" + "RATE_LIMIT_COST_STICKER")
	}()

	// should override by env
	readConfig()
	req.Equal(0, config.MaxToken)
	req.Equal(0.0, config.Limit)
	req.Equal(-1, config.Cost)
	req.Equal(-1, config.StickerCost)

	// should check to default
	checkConfig()
	req.Equal(1, config.MaxToken)
	req.Equal(1.0, config.Limit)
	req.Equal(1, config.Cost)
	req.Equal(1, config.StickerCost)

}
