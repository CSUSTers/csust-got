package config

import (
	"math"

	"github.com/spf13/viper"
)

// Model is the model configuration for chat
type Model struct {
	Name          string `mapstructure:"name"`
	BaseUrl       string `mapstructure:"base_url"`
	ApiKey        string `mapstructure:"api_key"`
	PromptLimit   int    `mapstructure:"prompt_limit"`
	Model         string `mapstructure:"model"`
	RetryNums     int    `mapstructure:"retry_nums"`
	RetryInterval int    `mapstructure:"retry_interval"`
	Proxy         string `mapstructure:"proxy"`

	Features ModelFeatures `mapstructure:"features"`
}

// ModelFeatures is the model features switch
type ModelFeatures struct {
	Image     bool `mapstructure:"image"`
	Mcp       bool `mapstructure:"mcp"`
	WhiteList bool `mapstructure:"white_list"`
}

// ChatTrigger is the configuration for chat
type ChatTrigger struct {
	Command string `mapstructure:"command"`
	Regex   string `mapstructure:"regex"`
	Reply   bool   `mapstructure:"reply"`
}

// ChatConfigV2 is the configuration for chat
type ChatConfigV2 []*ChatConfigSingle

// ChatConfigSingle is the configuration for a single chat
type ChatConfigSingle struct {
	Name           string         `mapstructure:"name"`
	Model          *Model         `mapstructure:"model"`
	MessageContext int            `mapstructure:"message_context"`
	Temperature    *float32       `mapstructure:"temperature"`
	PlaceHolder    string         `mapstructure:"place_holder"`
	ErrorMessage   string         `mapstructure:"error_message"` // 添加错误提示消息配置
	Steam          bool           `mapstructure:"stream"`
	SystemPrompt   string         `mapstructure:"system_prompt"`
	PromptTemplate string         `mapstructure:"prompt_template"`
	Trigger        []*ChatTrigger `mapstructure:"trigger"`

	Features FeatureSetting `mapstructure:"features"`
}

// TriggerOnReply checks if the chat will trigger on reply
func (ccs *ChatConfigSingle) TriggerOnReply() (*ChatTrigger, bool) {
	for _, t := range ccs.Trigger {
		if t.Reply {
			return t, true
		}
	}
	return nil, false
}

// FeatureSetting is the ~~Nintendo~~ switch and setting for model features
type FeatureSetting struct {
	Image              bool `mapstructure:"image"`
	ImageResizeSetting struct {
		MaxWidth     int  `mapstructure:"max_width"`
		MaxHeight    int  `mapstructure:"max_height"`
		NotKeepRatio bool `mapstructure:"not_keep_ratio"`
	} `mapstructure:"image_resize"`
}

// McpServers is the configuration for mcp servers
type McpServers []McpServerConfig

// McpServerConfig is the configuration for a single mcp server
type McpServerConfig struct {
	Name    string   `mapstructure:"name"`
	Command string   `mapstructure:"command"`
	Args    []string `mapstructure:"args"`
	Env     []string `mapstructure:"env"`
}

func (m *McpServers) readConfig() {
	v := viper.GetViper()
	err := v.UnmarshalKey("mcp_servers", m)
	if err != nil {
		panic(err)
	}
}

// ImageResize return the resized width and height for image
func (f *FeatureSetting) ImageResize(w, h int) (int, int) {
	mw, mh := f.ImageResizeSetting.MaxWidth, f.ImageResizeSetting.MaxHeight
	if mw <= 0 {
		mw = 512
	}
	if mh <= 0 {
		mh = 512
	}

	if f.ImageResizeSetting.NotKeepRatio {
		if w > mw {
			w = mw
		}
		if h > mh {
			h = mh
		}
	} else {
		ratio := float64(w) / float64(h)

		wOversize := float64(w) / float64(mw)
		hOversize := float64(h) / float64(mh)
		if wOversize > 1. || hOversize > 1. {
			if wOversize > hOversize {
				w = mw
				h = int(math.Round(float64(mw) / ratio))
			} else {
				h = mh
				w = int(math.Round(float64(mh) * ratio))
			}
		}
	}
	return w, h
}

// GetTemperature returns the temperature for the chat model
func (ccs *ChatConfigSingle) GetTemperature() float32 {
	if ccs.Temperature != nil {
		return *ccs.Temperature
	}
	return 1.0
}

// GetErrorMessage returns the error message for the chat model
func (ccs *ChatConfigSingle) GetErrorMessage() string {
	if ccs.ErrorMessage != "" {
		return ccs.ErrorMessage
	}
	return "😔很抱歉，我无法处理您的请求"
}

func (c *ChatConfigV2) readConfig() {
	v := viper.GetViper()
	err := v.UnmarshalKey("chats", c)
	if err != nil {
		panic(err)
	}

}
