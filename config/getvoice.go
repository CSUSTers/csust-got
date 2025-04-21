package config

import "github.com/spf13/viper"

// GetVoiceConfig is config
type GetVoiceConfig struct {
	Enable      bool          `mapstructure:"enable"`
	Meili       *MeiliSearch  `mapstructure:"meili"`
	ErrAudioUrl string        `mapstructure:"err_audio_url"`
	Indexes     []IndexConfig `mapstructure:"indexes"`
}

// MeiliSearch is config for meilisearch
type MeiliSearch struct {
	Host   string `mapstructure:"host"`
	ApiKey string `mapstructure:"api_key"`
}

// Database is config for database
type Database struct {
	Type string `mapstructure:"type"`
	File string `mapstructure:"file"`
}

// IndexConfig is config for index
type IndexConfig struct {
	Name     string   `mapstructure:"name"`
	Alias    []string `mapstructure:"alias"`
	IndexUid string   `mapstructure:"index_uid"`

	*Database `mapstructure:"database"`

	VoiceBaseUrl string `mapstructure:"voice_base_url"`
}

func (c *GetVoiceConfig) readConfig() {
	err := viper.UnmarshalKey("get_voice", c)
	if err != nil {
		panic(err)
	}
}

func (c *GetVoiceConfig) checkConfig() {
	// 修正字段名为 HostAddr
	if c.Enable && (BotConfig.MeiliConfig == nil || BotConfig.MeiliConfig.HostAddr == "") {
		panic("MeiliSearch URL is required when GetVoice is enabled")
	}
}
