package config

import (
	"os"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
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
	req.Equal("https://api.csu.st", BotConfig.GenShinConfig.ApiServer)
	req.Equal("https://api.csu.st/file/VO_inGame/VO_NPC/NPC_DQ/vo_npc_dq_f_katheryne_01.ogg", BotConfig.GenShinConfig.ErrAudioAddr)

	initViper("not_exist", "")
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
	t.Setenv(testEnvPrefix+"_"+"DEBUG", "true")
	t.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	t.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")

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
		t.Setenv(testEnvPrefix+"_"+""+v, v)
	}

	// all set should not panic
	BotConfig = NewBotConfig()
	initViper("", testEnvPrefix)
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

	// set some env
	t.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	t.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")
	// init config
	BotConfig = NewBotConfig()
	initViper(testConfigFile, testEnvPrefix)
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

	// set some env
	t.Setenv(testEnvPrefix+"_"+"TOKEN", "some-bot-token")
	t.Setenv(testEnvPrefix+"_"+"REDIS_ADDR", "some-env-address")

	// init config
	BotConfig = NewBotConfig()

	initViper(testConfigFile, testEnvPrefix)
	readConfig()

	defer viper.Reset()

	req.True(BotConfig.BlockListConfig.Enabled)
	req.True(BotConfig.WhiteListConfig.Enabled)
}

func TestChatConfigV2(t *testing.T) {
	req := testInit(t)

	// init config
	BotConfig = NewBotConfig()

	initViper(testConfigFile, testEnvPrefix)
	readConfig()

	defer viper.Reset()

	t.Logf("%+v", BotConfig.ChatConfigV2)
	req.Len(*BotConfig.ChatConfigV2, 1)
	req.NotNil((*BotConfig.ChatConfigV2)[0].Model)
	req.NotEmpty((*BotConfig.ChatConfigV2)[0].Model.Model)
}
