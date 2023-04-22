package config

import (
	"fmt"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

// BotConfig can get bot's config globally.
var BotConfig *Config

// Global koanf instance. Use "." as the key path delimiter.
var botKoanf *koanf.Koanf

var (
	noTokenMsg      = "bot token is not set! Please set config file config.yaml or env BOT_TOKEN!"
	noRedisMsg      = "redis address is not set! Please set config file config.yaml or env BOT_REDIS_ADDR!"
	noGenShinApiMsg = "genShinApi address is not set! Please set config file config.yaml!"
	noMeiliMsg      = "meili configuration is not set! Please set config file config.yaml!"
)

// interface for module config
// type config interface {
// 	readConfig()
// 	checkConfig()
// }

// InitConfig - init bot config.
func InitConfig(configFile, envPrefix string) {
	BotConfig = NewBotConfig()
	initKoanf(configFile, envPrefix)
	readConfig()
	checkConfig()
}

// NewBotConfig - return new bot config with all zero value.
// In general, you don't need to NewBotConfig, global BotConfig should be used.
func NewBotConfig() *Config {
	config := &Config{
		RateLimitConfig: new(rateLimitConfig),
		RedisConfig:     new(redisConfig),
		RestrictConfig:  new(restrictConfig),
		MessageConfig:   new(messageConfig),
		WhiteListConfig: new(specialListConfig),
		BlockListConfig: new(specialListConfig),
		PromConfig:      new(promConfig),
		GenShinConfig:   new(genShinConfig),
		ChatConfig:      new(chatConfig),
		MeiliConfig:     new(meiliConfig),
		McConfig:        new(mcConfig),
	}

	config.WhiteListConfig.SetName("white_list")
	config.BlockListConfig.SetName("block_list")

	return config
}

// Config the interface for common configs.
type Config struct {
	Bot *Bot `koanf:"-"`

	Token        string `koanf:"token"`
	Proxy        string `koanf:"proxy"`
	Listen       string `koanf:"listen"`
	DebugMode    bool   `koanf:"debug"`
	SkipDuration int64  `koanf:"skip_duration"`

	RedisConfig     *redisConfig       `koanf:"redis"`
	RestrictConfig  *restrictConfig    `koanf:"restrict"`
	RateLimitConfig *rateLimitConfig   `koanf:"rate_limit"`
	MessageConfig   *messageConfig     `koanf:"message"`
	BlockListConfig *specialListConfig `koanf:"block_list"`
	WhiteListConfig *specialListConfig `koanf:"white_list"`
	PromConfig      *promConfig        `koanf:"prometheus"`
	GenShinConfig   *genShinConfig     `koanf:"genshin_voice"`
	ChatConfig      *chatConfig        `koanf:"chatgpt"`
	MeiliConfig     *meiliConfig       `koanf:"meili"`
	McConfig        *mcConfig          `koanf:"mc"`
}

// GetBot returns Bot.
func GetBot() *Bot {
	return BotConfig.Bot
}

func initKoanf(configFile, envPrefix string) {
	botKoanf = koanf.New(".")

	if configFile != "" {
		if err := botKoanf.Load(file.Provider(configFile), yaml.Parser()); err != nil {
			zap.L().Warn("an error was produced when reading file config!", zap.String("configFile", configFile), zap.Error(err))
		}
	}

	if envPrefix != "" {
		if !strings.HasSuffix(envPrefix, "_") {
			envPrefix += "_"
		}
		err := botKoanf.Load(env.Provider(envPrefix, ".", func(s string) string {
			return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, envPrefix)), "__", ".")
		}), nil)
		if err != nil {
			zap.L().Warn("an error was produced when reading env config!", zap.String("envPrefix", envPrefix), zap.Error(err))
		}
	}

	noTokenMsg = fmt.Sprintf("bot token is not set! Please set config file %s or env %s_TOKEN!", configFile, envPrefix)
	noRedisMsg = fmt.Sprintf("redis address is not set! Please set config file %s or env %s_REDIS_ADDR!", configFile, envPrefix)
}

func readConfig() {
	err := botKoanf.Unmarshal("", &BotConfig)
	if err != nil {
		zap.L().Panic("unmarshal config failed", zap.Error(err))
	}
}

// check some config value is reasonable, otherwise set to default value.
func checkConfig() {
	if BotConfig.Token == "" {
		zap.L().Panic(noTokenMsg)
	}
	if BotConfig.DebugMode {
		zap.L().Warn("DEBUG MODE IS ON")
	}
	if BotConfig.SkipDuration < 0 {
		BotConfig.SkipDuration = 0
	}

	BotConfig.RedisConfig.checkConfig()
	BotConfig.RestrictConfig.checkConfig()
	BotConfig.RateLimitConfig.checkConfig()
	BotConfig.MessageConfig.checkConfig()
	BotConfig.BlockListConfig.checkConfig()
	BotConfig.WhiteListConfig.checkConfig()
	BotConfig.PromConfig.checkConfig()
	BotConfig.GenShinConfig.checkConfig()
	BotConfig.ChatConfig.checkConfig()
	BotConfig.MeiliConfig.checkConfig()
	BotConfig.McConfig.checkConfig()
}
