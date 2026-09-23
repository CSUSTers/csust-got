package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
	"gopkg.in/yaml.v3"
)

// BotConfig can get bot's config globally.
var BotConfig *Config

var runtimeEnvConfig map[string]string
var runtimeEnvConfigErr error

var (
	noTokenMsg = "bot token is not set! Please set config file config.yaml or env BOT_TOKEN!"
	noRedisMsg = "redis address is not set! Please set config file config.yaml or env BOT_REDIS_ADDR!"
	noMeiliMsg = "meili configuration is not set! Please set config file config.yaml!"
)

// interface for module config
// type config interface {
// 	readConfig()
// 	checkConfig()
// }

// InitConfig - init bot config.
func InitConfig(configFile, envPrefix string) {
	BotConfig = NewBotConfig()
	InitViper(configFile, envPrefix)
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
		GetVoiceConfig:  new(GetVoiceConfig),
		MeiliConfig:     new(meiliConfig),
		McConfig:        new(mcConfig),
		DebugOptConfig:  new(debugOptConfig),
		Agents:          new(AgentV3Configs),
		AgentV3:         new(AgentV3Config),
	}

	config.WhiteListConfig.SetName("white_list")
	config.BlockListConfig.SetName("black_list")

	return config
}

// Config the interface for common configs.
type Config struct {
	Bot *Bot

	URL          string
	Token        string
	Proxy        string
	Listen       string
	DebugMode    bool
	SkipDuration int64
	LogFileDir   string

	// SentenceDelimiters for intelligent sentence breaking in streaming
	SentenceDelimiters []string

	RedisConfig     *redisConfig
	RestrictConfig  *restrictConfig
	RateLimitConfig *rateLimitConfig
	MessageConfig   *messageConfig
	BlockListConfig *specialListConfig
	WhiteListConfig *specialListConfig
	*GetVoiceConfig
	Agents      *AgentV3Configs
	AgentV3     *AgentV3Config
	MeiliConfig *meiliConfig
	McConfig    *mcConfig

	DebugOptConfig *debugOptConfig
}

// GetBot returns Bot.
func GetBot() *Bot {
	return BotConfig.Bot
}

// InitViper init viper
func InitViper(configFile, envPrefix string) {
	runtimeEnvConfig = nil
	runtimeEnvConfigErr = nil
	if configFile != "" {
		baseEnv, err := readRuntimeEnvFile(configFile)
		if err != nil {
			runtimeEnvConfigErr = err
		}
		mergeRuntimeEnvEntries(baseEnv)
		if err := checkRuntimeEnvLookups(runtimeEnvConfig); err != nil {
			runtimeEnvConfigErr = err
		}
		viper.SetConfigFile(configFile)
		if err := viper.ReadInConfig(); err != nil {
			if len(baseEnv) > 0 && runtimeEnvConfigErr == nil {
				runtimeEnvConfigErr = errors.New("runtime_env_load")
			}
			if runtimeEnvConfigErr != nil {
				zap.L().Warn("an error was produced when reading config!", zap.String("configFile", configFile), zap.Error(runtimeEnvConfigErr))
			} else {
				zap.L().Warn("an error was produced when reading config!", zap.String("configFile", configFile), zap.Error(err))
			}
			return
		}
		// 检查同一目录下是否存在custom.yaml文件
		customConfigFile := filepath.Join(filepath.Dir(configFile), "custom.yaml")
		if _, err := os.Stat(customConfigFile); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				runtimeEnvConfigErr = errors.New("runtime_env_load")
				zap.L().Warn("an error was produced when reading custom config!", zap.String("customConfigFile", customConfigFile), zap.Error(err))
			}
		} else {
			customEnv, envErr := readRuntimeEnvFile(customConfigFile)
			if envErr != nil {
				runtimeEnvConfigErr = envErr
			}
			mergeRuntimeEnvEntries(customEnv)
			if err := checkRuntimeEnvLookups(runtimeEnvConfig); err != nil {
				runtimeEnvConfigErr = err
			}
			v := viper.New()
			v.SetConfigFile(customConfigFile)
			if err := v.ReadInConfig(); err != nil {
				if len(customEnv) > 0 && runtimeEnvConfigErr == nil {
					runtimeEnvConfigErr = errors.New("runtime_env_load")
				}
				if runtimeEnvConfigErr != nil {
					zap.L().Warn("an error was produced when reading custom config!", zap.String("customConfigFile", customConfigFile), zap.Error(runtimeEnvConfigErr))
				} else {
					zap.L().Warn("an error was produced when reading custom config!", zap.String("customConfigFile", customConfigFile), zap.Error(err))
				}
			} else if err := viper.MergeConfigMap(v.AllSettings()); err != nil {
				if len(customEnv) > 0 && runtimeEnvConfigErr == nil {
					runtimeEnvConfigErr = errors.New("runtime_env_load")
				}
				if runtimeEnvConfigErr != nil {
					zap.L().Warn("an error was produced when merging custom config!", zap.String("customConfigFile", customConfigFile), zap.Error(runtimeEnvConfigErr))
				} else {
					zap.L().Warn("an error was produced when merging custom config!", zap.String("customConfigFile", customConfigFile), zap.Error(err))
				}
			} else {
				if runtimeEnvConfigErr == nil {
					zap.L().Info("custom config merged successfully", zap.String("customConfigFile", customConfigFile))
				}
			}
		}
	}
	viper.SetEnvPrefix(envPrefix)
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AllowEmptyEnv(true)
	viper.AutomaticEnv()
	if runtimeEnvConfigErr == nil {
		for name := range runtimeEnvConfig {
			lookup := "AGENT_V3_RUNTIME_ENV_" + strings.ToUpper(name)
			if envPrefix != "" {
				lookup = envPrefix + "_" + lookup
			}
			if value, ok := os.LookupEnv(lookup); ok {
				runtimeEnvConfig[name] = value
			}
		}
	}

	noTokenMsg = fmt.Sprintf("bot token is not set! Please set config file %s or env %s_TOKEN!", configFile, envPrefix)
	noRedisMsg = fmt.Sprintf("redis address is not set! Please set config file %s or env %s_REDIS_ADDR!", configFile, envPrefix)
}

func checkRuntimeEnvLookups(entries map[string]string) error {
	lookups := make(map[string]bool, len(entries))
	for name := range entries {
		lookup := strings.ToUpper(name)
		if lookups[lookup] {
			return errors.New("runtime_env_lookup_collision")
		}
		lookups[lookup] = true
	}
	return nil
}

func readRuntimeEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("runtime_env_load")
	}
	return parseRuntimeEnvYAML(data)
}

func mergeRuntimeEnvEntries(entries map[string]string) {
	if len(entries) > 0 && runtimeEnvConfig == nil {
		runtimeEnvConfig = make(map[string]string, len(entries))
	}
	for name, value := range entries {
		runtimeEnvConfig[name] = value
	}
}

func parseRuntimeEnvYAML(data []byte) (map[string]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, errors.New("runtime_env_load")
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	node := doc.Content[0]
	for _, section := range [...]string{"agent_v3", "runtime", "env"} {
		if node.Kind != yaml.MappingNode {
			return nil, errors.New("runtime_env_type")
		}
		var next *yaml.Node
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Tag == "!!merge" {
				return nil, errors.New("runtime_env_merge")
			}
			if key.Kind == yaml.ScalarNode && strings.EqualFold(key.Value, section) {
				if key.Value != section {
					return nil, errors.New("runtime_env_type")
				}
				if next != nil {
					return nil, errors.New("runtime_env_duplicate")
				}
				next = node.Content[i+1]
			}
		}
		if next == nil {
			return nil, nil
		}
		node = next
	}
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("runtime_env_type")
	}
	result := make(map[string]string, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag == "!!merge" {
			return nil, errors.New("runtime_env_merge")
		}
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return nil, errors.New("runtime_env_type")
		}
		if _, exists := result[key.Value]; exists {
			return nil, errors.New("runtime_env_duplicate")
		}
		result[key.Value] = value.Value
	}
	return result, nil
}

func readConfig() {
	// base config
	BotConfig.DebugMode = viper.GetBool("debug")
	BotConfig.URL = viper.GetString("url")
	BotConfig.Token = viper.GetString("token")
	BotConfig.Proxy = viper.GetString("proxy")
	BotConfig.Listen = viper.GetString("listen")
	BotConfig.SkipDuration = viper.GetInt64("skip_duration")
	BotConfig.LogFileDir = viper.GetString("log_file_dir")

	// sentence delimiters for streaming
	BotConfig.SentenceDelimiters = viper.GetStringSlice("sentence_delimiters")
	if len(BotConfig.SentenceDelimiters) == 0 {
		// Set default sentence delimiters if none provided
		BotConfig.SentenceDelimiters = []string{
			"\n", ".", "!", "?", "。", "！", "？", ")", "）", ";",
		}
	}

	// other
	BotConfig.RedisConfig.readConfig()
	BotConfig.RestrictConfig.readConfig()
	BotConfig.RateLimitConfig.readConfig()
	BotConfig.MessageConfig.readConfig()
	BotConfig.WhiteListConfig.readConfig()
	BotConfig.BlockListConfig.readConfig()
	BotConfig.MeiliConfig.readConfig()
	BotConfig.McConfig.readConfig()
	BotConfig.Agents.readConfig()
	BotConfig.AgentV3.readConfig()

	// genshin voice
	BotConfig.readConfig()

	// debug opt
	BotConfig.DebugOptConfig.readConfig()
}

// ReadConfig read config.
func ReadConfig(configs ...interface{ readConfig() }) {
	for _, config := range configs {
		config.readConfig()
	}
}

// check some config value is reasonable, otherwise set to default value.
func checkConfig() {
	if runtimeEnvConfigErr != nil {
		zap.L().Panic("invalid agent_v3 runtime env", zap.Error(runtimeEnvConfigErr))
	}
	if BotConfig.Token == "" {
		zap.L().Panic(noTokenMsg)
	}
	if BotConfig.DebugMode {
		zap.L().Warn("DEBUG MODE IS ON")
	}
	if BotConfig.SkipDuration < 0 {
		BotConfig.SkipDuration = 0
	}

	BotConfig.LogFileDir = strings.TrimRight(BotConfig.LogFileDir, "/")

	BotConfig.RedisConfig.checkConfig()
	BotConfig.RestrictConfig.checkConfig()
	BotConfig.RateLimitConfig.checkConfig()
	BotConfig.MessageConfig.checkConfig()
	BotConfig.BlockListConfig.checkConfig()
	BotConfig.WhiteListConfig.checkConfig()
	BotConfig.checkConfig()
	BotConfig.MeiliConfig.checkConfig()
	BotConfig.McConfig.checkConfig()
	BotConfig.AgentV3.checkConfig()
	if err := ValidateAgentV3RuntimeEnv(BotConfig.AgentV3.Runtime.Env); err != nil {
		zap.L().Panic("invalid agent_v3 runtime env", zap.Error(err))
	}
	if BotConfig.Agents != nil {
		for _, agent := range *BotConfig.Agents {
			if agent == nil {
				continue
			}
			if err := agent.ValidateContextMode(); err != nil {
				zap.L().Panic("invalid agent config", zap.Error(err))
			}
		}
	}

	BotConfig.DebugOptConfig.checkConfig()
}
