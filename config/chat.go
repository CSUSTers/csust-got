package config

import (
	"log"
	"math"
	"strings"
	"time"

	"github.com/samber/lo"
	"github.com/spf13/viper"
	"go.uber.org/zap"
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
	Gacha   int    `mapstructure:"gacha"`
}

// ChatOutputFormatConfig is the configuration for tg message format
type ChatOutputFormatConfig struct {
	// format: markdown(default), html
	Format string `mapstructure:"format"`
	// how to show the reason output: none(default), quote, collapse
	Reason string `mapstructure:"reason"`
	// how to show the payload output: plain(default), quote, collapse, block
	Payload string `mapstructure:"payload"`
	// stream_output: enable streaming typewriter effect (false by default)
	StreamOutput bool `mapstructure:"stream_output"`
	// edit_interval: minimum time interval between message edits for rate limiting
	EditInterval string `mapstructure:"edit_interval"`
	// use_native_reasoning: use native OpenAI protocol ReasoningContent field (true by default)
	// When false, falls back to parsing <think>...</think> tags from response text
	UseNativeReasoning *bool `mapstructure:"use_native_reasoning"`
	// ProgressSummary configures progress summarization during agent execution.
	// When enabled, the agent can call update_progress to send status updates
	// to the user via a small/cheap model.
	ProgressSummary *ProgressSummaryConfig `mapstructure:"progress_summary"`
}

// ProgressSummaryConfig configures the progress summarization feature.
// A small model processes the agent's progress updates before displaying to the user.
type ProgressSummaryConfig struct {
	// Enable turns on progress summarization
	Enable bool `mapstructure:"enable"`
	// Model is the small/cheap model used for summarizing progress (optional).
	// If nil, the agent's raw update_progress content is displayed directly.
	Model *Model `mapstructure:"model"`
	// Prompt is the system prompt for the summarizer model.
	Prompt JoinableString `mapstructure:"prompt"`
}

const (
	// OutputFormatMarkdown is the markdown format type
	OutputFormatMarkdown = "markdown"
	// OutputFormatHTML is the HTML format type
	OutputFormatHTML = "html"
)

// GetFormat get message format
func (c *ChatOutputFormatConfig) GetFormat() string {
	switch strings.ToLower(c.Format) {
	case "", "md", "mdv2", "markdown", "markdownv2":
		return OutputFormatMarkdown
	case "html":
		return OutputFormatHTML
	default:
		return ""
	}
}

// GetReasonFormat get reason output format
//
// nolint: goconst
func (c *ChatOutputFormatConfig) GetReasonFormat() string {
	switch strings.ToLower(c.Reason) {
	case "", "none", "false":
		return "none"
	case "quote", "q":
		return "quote"
	case "collapse", "c":
		return "collapse"
	default:
		return ""
	}
}

// GetPayloadFormat get payload output format
//
// nolint: goconst
func (c *ChatOutputFormatConfig) GetPayloadFormat() string {
	switch strings.ToLower(c.Payload) {
	case "", "plain", "p":
		return "plain"
	case "quote", "q":
		return "quote"
	case "collapse", "c":
		return "collapse"
	case "block", "b":
		return "block"
	case "md", "md-block", "markdown", "markdown-block":
		return "markdown-block"
	default:
		return ""
	}
}

// GetEditInterval returns the edit interval as a time.Duration
func (c *ChatOutputFormatConfig) GetEditInterval() time.Duration {
	if c.EditInterval == "" {
		return time.Second
	}
	d, err := time.ParseDuration(c.EditInterval)
	if err != nil {
		return time.Second
	}
	return d
}

// GetUseNativeReasoning returns whether to use native OpenAI protocol reasoning (default: true)
func (c *ChatOutputFormatConfig) GetUseNativeReasoning() bool {
	if c.UseNativeReasoning == nil {
		return true // default to using native reasoning
	}
	return *c.UseNativeReasoning
}

// ChatFilterConfig represents the configuration for a filter
type ChatFilterConfig struct {
	// Type is the type of filter (e.g., "whitelist")
	Type string `mapstructure:"type"`

	// Whitelist filter configuration
	Whitelist []int64 `mapstructure:"whitelist,omitempty"`
}

// ChatFilterSetting represents the filter settings for a chat configuration
type ChatFilterSetting struct {
	// Filters is a list of filters to apply in order
	Filters []ChatFilterConfig `mapstructure:"filters"`
}

// ChatConfigV1 is the configuration for chat
type ChatConfigV1 []*ChatConfigSingle

// ChatConfigV2 is the configuration for agent
type ChatConfigV2 []*ChatConfigSingle

// ChatConfigSingle is the configuration for a single chat
type ChatConfigSingle struct {
	Name            string                 `mapstructure:"name"`
	Model           *Model                 `mapstructure:"model"`
	MessageContext  int                    `mapstructure:"message_context"`
	Temperature     *float32               `mapstructure:"temperature"`
	PlaceHolder     string                 `mapstructure:"place_holder"`
	ErrorMessage    string                 `mapstructure:"error_message"` // 添加错误提示消息配置
	SystemPrompt    JoinableString         `mapstructure:"system_prompt"`
	PromptTemplate  JoinableString         `mapstructure:"prompt_template"`
	Trigger         []*ChatTrigger         `mapstructure:"trigger"`
	Timeout         int                    `mapstructure:"timeout"` // seconds
	Format          ChatOutputFormatConfig `mapstructure:"format"`
	ReasoningEffort string                 `mapstructure:"reasoning_effort"`

	Agent    *AgentConfig      `mapstructure:"agent"`
	Features FeatureSetting    `mapstructure:"features"`
	UseMcpo  bool              `mapstructure:"use_mcpo"`
	Filters  ChatFilterSetting `mapstructure:"filters"`
}

// SubAgentConfig defines a subagent that can be invoked by the main agent as a tool
type SubAgentConfig struct {
	Name         string            `mapstructure:"name"`
	Description  string            `mapstructure:"description"`
	Model        *Model            `mapstructure:"model"`
	SystemPrompt JoinableString    `mapstructure:"system_prompt"`
	Tools        []string          `mapstructure:"tools"`
	MaxSteps     int               `mapstructure:"max_steps"`
	McpServers   []*McpoConfig     `mapstructure:"mcp_servers"`
	ToolModels   map[string]*Model `mapstructure:"tool_models"`
}

// GetMaxSteps returns the max tool call steps for the subagent
func (c *SubAgentConfig) GetMaxSteps() int {
	if c != nil && c.MaxSteps > 0 {
		return c.MaxSteps
	}
	return 5
}

// AgentConfig defines the agent mode configuration for chatv2
type AgentConfig struct {
	Enable     bool              `mapstructure:"enable"`
	Tools      []string          `mapstructure:"tools"`
	MaxSteps   int               `mapstructure:"max_steps"`
	SubAgents  []*SubAgentConfig `mapstructure:"subagents"`
	McpServers []*McpoConfig     `mapstructure:"mcp_servers"`
	ToolModels map[string]*Model `mapstructure:"tool_models"`
}

// GetMaxSteps returns the max tool call steps for the main agent
func (c *AgentConfig) GetMaxSteps() int {
	if c != nil && c.MaxSteps > 0 {
		return c.MaxSteps
	}
	return 12
}

// IsAgentEnabled returns true if chatv2 agent mode is enabled for this chat config
func (ccs *ChatConfigSingle) IsAgentEnabled() bool {
	return ccs.Agent != nil && ccs.Agent.Enable
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

// TriggerForGacha checks if the chat will trigger on gacha
func (ccs *ChatConfigSingle) TriggerForGacha() ([]int, bool) {
	stars := lo.FilterMap(ccs.Trigger, func(t *ChatTrigger, _ int) (int, bool) {
		if t.Gacha > 0 {
			return t.Gacha, true
		}
		return 0, false
	})
	return stars, len(stars) > 0
}

// GetTimeout returns the timeout for the chat model
func (ccs *ChatConfigSingle) GetTimeout() time.Duration {
	if ccs.Timeout > 0 {
		return time.Duration(ccs.Timeout) * time.Second
	}
	return 30 * time.Second
}

// FeatureSetting is the ~~Nintendo~~ switch and setting for model features
type FeatureSetting struct {
	Image              bool `mapstructure:"image"`
	ImageResizeSetting struct {
		MaxWidth     int  `mapstructure:"max_width"`
		MaxHeight    int  `mapstructure:"max_height"`
		NotKeepRatio bool `mapstructure:"not_keep_ratio"`
	} `mapstructure:"image_resize"`
	AllowRegenerate    bool   `mapstructure:"allow_regenerate"`     // Allow regeneration on 👎 reaction
	MaxRegenerateCount int    `mapstructure:"max_regenerate_count"` // Maximum number of regenerations allowed
	RegenerateFeedback string `mapstructure:"regenerate_feedback"`  // User feedback message for regeneration
}

// McpoConfig is the configuration for mcpo server
type McpoConfig struct {
	Enable bool     `mapstructure:"enable"`
	Url    string   `mapstructure:"url"`
	Tools  []string `mapstructure:"tools"`
	ApiKey string   `mapstructure:"api_key"` // Optional API key for MCP servers
}

func (c *McpoConfig) readConfig() {
	err := viper.UnmarshalKey("mcpo_server", c)
	if err != nil {
		log.Fatal("cannot parse mcpo config", zap.Error(err))
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

// GetMaxRegenerateCount returns the maximum regeneration count for the chat model
func (f *FeatureSetting) GetMaxRegenerateCount() int {
	if f.MaxRegenerateCount > 0 {
		return f.MaxRegenerateCount
	}
	return 3 // default value
}

// GetRegenerateFeedback returns the user feedback message for regeneration
func (f *FeatureSetting) GetRegenerateFeedback() string {
	if f.RegenerateFeedback != "" {
		return f.RegenerateFeedback
	}
	return "用户认为上次的回答👎" // default message
}

func (c *ChatConfigV1) readConfig() {
	v := viper.GetViper()
	err := v.UnmarshalKey("chats", c, viper.DecodeHook(DispatchFor()))
	if err != nil {
		panic(err)
	}

}

func (c *ChatConfigV2) readConfig() {
	v := viper.GetViper()
	err := v.UnmarshalKey("agents", c, viper.DecodeHook(DispatchFor()))
	if err != nil {
		zap.L().Warn("cannot parse agents config", zap.Error(err))
		return
	}
	// Auto-enable agent mode for each entry in agents[]
	for _, cfg := range *c {
		if cfg.Agent == nil {
			cfg.Agent = &AgentConfig{Enable: true}
		}
	}
	}
