package config

import (
	"github.com/spf13/viper"
)

type Model struct {
	Name          string `mapstructure:"name"`
	BaseUrl       string `mapstructure:"base_url"`
	ApiKey        string `mapstructure:"api_key"`
	PromptLimit   int    `mapstructure:"prompt_limit"`
	Model         string `mapstructure:"model"`
	RetryNums     int    `mapstructure:"retry_nums"`
	RetryInterval int    `mapstructure:"retry_interval"`
	Proxy         string `mapstructure:"proxy"`
}

type ChatTrigger struct {
	Command string `mapstructure:"command"`
	Regex   string `mapstructure:"regex"`
}

type ChatConfigV2 []*ChatConfigSingle

type ChatConfigSingle struct {
	Name           string         `mapstructure:"name"`
	Model          *Model         `mapstructure:"model"`
	MessageContext int            `mapstructure:"message_context"`
	Temperature    *float32       `mapstructure:"temperature"`
	PlaceHolder    string         `mapstructure:"place_holder"`
	Steam          bool           `mapstructure:"steam"`
	SystemPrompt   string         `mapstructure:"system_prompt"`
	PromptTemplate string         `mapstructure:"prompt_template"`
	Trigger        []*ChatTrigger `mapstructure:"trigger"`
}

func (ccs *ChatConfigSingle) GetTemperature() float32 {
	if ccs.Temperature != nil {
		return *ccs.Temperature
	}
	return 1.0
}

func (c *ChatConfigV2) readConfig() {
	v := viper.GetViper()
	err := v.UnmarshalKey("chats", c)
	if err != nil {
		panic(err)
	}

}

type GachaConfig struct {
}
