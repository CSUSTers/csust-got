package config

import (
	"errors"
	"log"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/samber/lo"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// Model is the model configuration for chat
type Model struct {
	Name                 string `mapstructure:"name"`
	BaseUrl              string `mapstructure:"base_url"`
	ApiKey               string `mapstructure:"api_key"`
	PromptLimit          int    `mapstructure:"prompt_limit"`
	Model                string `mapstructure:"model"`
	RetryNums            int    `mapstructure:"retry_nums"`
	RetryInterval        int    `mapstructure:"retry_interval"`
	RetryInitialInterval string `mapstructure:"retry_initial_interval"`
	Proxy                string `mapstructure:"proxy"`

	Features ModelFeatures `mapstructure:"features"`
}

// ModelFeatures is the model features switch
type ModelFeatures struct {
	Image          bool `mapstructure:"image"`
	ImageBase64Raw bool `mapstructure:"image_base64_raw"` // send raw base64 instead of data URI
	Mcp            bool `mapstructure:"mcp"`
	WhiteList      bool `mapstructure:"white_list"`
}

// RetryCount returns how many extra model-generation attempts are allowed after the first failure.
func (m *Model) RetryCount() int {
	if m == nil || m.RetryNums <= 0 {
		return defaultModelRetryNums
	}
	return m.RetryNums
}

// RetryInitialDelay returns the first exponential-backoff delay for retryable model errors.
func (m *Model) RetryInitialDelay() time.Duration {
	if m == nil {
		return defaultModelRetryInitialInterval
	}
	if d := parseFlexibleDuration(m.RetryInitialInterval, 0); d > 0 {
		return d
	}
	if m.RetryInterval > 0 {
		return time.Duration(m.RetryInterval) * time.Second
	}
	return defaultModelRetryInitialInterval
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

	defaultSubAgentMaxSteps             = 5
	defaultAgentMaxSteps                = 12
	defaultModelRetryNums               = 3
	defaultModelRetryInitialInterval    = 500 * time.Millisecond
	minToolAgentMaxSteps                = 4
	agentV3DefaultScope                 = "group"
	agentV3DefaultMemoryWritePolicy     = "explicit_or_admin"
	agentV3DefaultRuntimeMode           = "remote_http"
	agentV3DefaultSkillsMode            = "system_prompt"
	agentV3DefaultRuntimeEndpoint       = "http://agent-runtime:8080"
	agentV3DefaultCommandTimeout        = "120s"
	agentV3DefaultObservabilityJSONL    = "logs/agentv3-traces.jsonl"
	agentV3DefaultCaptureContent        = "preview"
	agentV3DefaultContextCacheRedisTTL  = "30d"
	agentV3DefaultSearXNGTimeout        = "10s"
	agentV3DefaultSearXNGMaxBody        = int64(1024 * 1024)
	agentV3DefaultSearXNGMaxResults     = 10
	agentV3DefaultSearXNGMaxResultChars = 2000
	agentV3DefaultSearXNGLanguage       = "zh-CN"
	agentV3DefaultSearXNGFormat         = "text"
	agentV3DefaultSearXNGUserAgent      = "csust-got-agent-v3"
)

var agentV3FixedTools = []string{"read", "grep", "write", "edit", "bash"}

var agentV3EnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var (
	errInvalidAgentV3SearXNGBaseURL                = errors.New("invalid agent_v3.skills.searxng.base_url")
	errInvalidAgentV3SearXNGCredentialsEnvironment = errors.New("invalid agent_v3.skills.searxng credentials environment")
	errInvalidAgentV3SearXNGTimeout                = errors.New("invalid agent_v3.skills.searxng.timeout")
	errInvalidAgentV3SearXNGMaxResponseBytes       = errors.New("invalid agent_v3.skills.searxng.max_response_bytes")
	errInvalidAgentV3SearXNGMaxResults             = errors.New("invalid agent_v3.skills.searxng.max_results")
	errInvalidAgentV3SearXNGMaxResultChars         = errors.New("invalid agent_v3.skills.searxng.max_result_chars")
	errInvalidAgentV3SearXNGDefaultLanguage        = errors.New("invalid agent_v3.skills.searxng.default_language")
	errInvalidAgentV3SearXNGDefaultSafeSearch      = errors.New("invalid agent_v3.skills.searxng.default_safesearch")
	errInvalidAgentV3SearXNGDefaultResponseFormat  = errors.New("invalid agent_v3.skills.searxng.default_response_format")
	errInvalidAgentV3SearXNGUserAgent              = errors.New("invalid agent_v3.skills.searxng.user_agent")
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
	Name         string              `mapstructure:"name"`
	Description  string              `mapstructure:"description"`
	Model        *Model              `mapstructure:"model"`
	SystemPrompt JoinableString      `mapstructure:"system_prompt"`
	Tools        []string            `mapstructure:"tools"`
	MaxSteps     int                 `mapstructure:"max_steps"`
	McpServers   []*ToolServerConfig `mapstructure:"mcp_servers"`
	ToolModels   map[string]*Model   `mapstructure:"tool_models"`
}

// GetMaxSteps returns the max tool call steps for the subagent
func (c *SubAgentConfig) GetMaxSteps() int {
	if c == nil {
		return defaultSubAgentMaxSteps
	}

	maxSteps := c.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultSubAgentMaxSteps
	}
	if c.usesTools() && maxSteps < minToolAgentMaxSteps {
		return minToolAgentMaxSteps
	}
	return maxSteps
}

// SkillConfig defines a reusable skill bundle that can be referenced by agents.
// A skill bundles together tools, MCP servers, system prompt additions, and tool model overrides.
type SkillConfig struct {
	Name              string              `mapstructure:"name"`
	Tools             []string            `mapstructure:"tools"`
	McpServers        []*ToolServerConfig `mapstructure:"mcp_servers"`
	SystemPromptAddon JoinableString      `mapstructure:"system_prompt_addon"`
	ToolModels        map[string]*Model   `mapstructure:"tool_models"`
}

// AgentConfig defines the agent mode configuration for chatv2
type AgentConfig struct {
	Enable     bool                `mapstructure:"enable"`
	V3         bool                `mapstructure:"v3"`
	Rich       bool                `mapstructure:"rich"`
	Tools      []string            `mapstructure:"tools"`
	MaxSteps   int                 `mapstructure:"max_steps"`
	SubAgents  []*SubAgentConfig   `mapstructure:"subagents"`
	McpServers []*ToolServerConfig `mapstructure:"mcp_servers"`
	ToolModels map[string]*Model   `mapstructure:"tool_models"`
	Skills     []*SkillConfig      `mapstructure:"skills"`
}

// AgentV3Config defines global agent-v3 defaults and runtime settings.
type AgentV3Config struct {
	Enable        bool                       `mapstructure:"enable"`
	Model         *Model                     `mapstructure:"model"`
	SoulPath      string                     `mapstructure:"soul_path"`
	ContextCache  AgentV3ContextCacheConfig  `mapstructure:"context_cache"`
	Memory        AgentV3MemoryConfig        `mapstructure:"memory"`
	Runtime       AgentV3RuntimeConfig       `mapstructure:"runtime"`
	Tools         AgentV3ToolsConfig         `mapstructure:"tools"`
	Skills        AgentV3SkillsConfig        `mapstructure:"skills"`
	Observability AgentV3ObservabilityConfig `mapstructure:"observability"`
}

// AgentV3ContextCacheConfig controls agent-v3 prompt cache and history windows.
type AgentV3ContextCacheConfig struct {
	Enable               bool   `mapstructure:"enable"`
	RawTurns             int    `mapstructure:"raw_turns"`
	SummaryTurns         int    `mapstructure:"summary_turns"`
	MaxSummaryTokens     int    `mapstructure:"max_summary_tokens"`
	MaxRawTokens         int    `mapstructure:"max_raw_tokens"`
	PromptCacheRetention string `mapstructure:"prompt_cache_retention"`
	RedisTTL             string `mapstructure:"redis_ttl"`
}

// AgentV3MemoryConfig controls chat-scoped agent-v3 memory.
type AgentV3MemoryConfig struct {
	Enable            bool   `mapstructure:"enable"`
	Scope             string `mapstructure:"scope"`
	AllowGlobal       bool   `mapstructure:"allow_global"`
	SnapshotMaxTokens int    `mapstructure:"snapshot_max_tokens"`
	WritePolicy       string `mapstructure:"write_policy"`
}

// AgentV3RuntimeConfig points agent-v3 tools at the remote runtime service.
type AgentV3RuntimeConfig struct {
	Enable         bool   `mapstructure:"enable"`
	Mode           string `mapstructure:"mode"`
	Endpoint       string `mapstructure:"endpoint"`
	AuthTokenEnv   string `mapstructure:"auth_token_env"`
	NamespaceScope string `mapstructure:"namespace_scope"`
	CommandTimeout string `mapstructure:"command_timeout"`
	MaxOutputChars int    `mapstructure:"max_output_chars"`
	RequestTimeout string `mapstructure:"request_timeout"`
	FetchEnabled   *bool  `mapstructure:"fetch_enabled"`
}

// AgentV3ToolsConfig constrains agent-v3 visible tools.
type AgentV3ToolsConfig struct {
	ExposeOnly []string `mapstructure:"expose_only"`
}

// AgentV3SkillsConfig configures agent-v3 system-prompt skill injection.
type AgentV3SkillsConfig struct {
	Mode          string               `mapstructure:"mode"`
	Root          string               `mapstructure:"root"`
	InjectBuiltin *bool                `mapstructure:"inject_builtin"`
	RuntimeGlobal bool                 `mapstructure:"runtime_global"`
	SearXNG       AgentV3SearXNGConfig `mapstructure:"searxng"`
}

// AgentV3SearXNGConfig configures the built-in agent-v3 SearXNG skill.
type AgentV3SearXNGConfig struct {
	Enable                bool   `mapstructure:"enable"`
	BaseURL               string `mapstructure:"base_url"`
	UsernameEnv           string `mapstructure:"username_env"`
	PasswordEnv           string `mapstructure:"password_env"`
	Timeout               string `mapstructure:"timeout"`
	MaxResponseBytes      int64  `mapstructure:"max_response_bytes"`
	MaxResults            int    `mapstructure:"max_results"`
	MaxResultChars        int    `mapstructure:"max_result_chars"`
	DefaultLanguage       string `mapstructure:"default_language"`
	DefaultSafeSearch     int    `mapstructure:"default_safesearch"`
	DefaultResponseFormat string `mapstructure:"default_response_format"`
	UserAgent             string `mapstructure:"user_agent"`
}

// AgentV3ObservabilityConfig controls agent-v3 trace capture.
type AgentV3ObservabilityConfig struct {
	Enable         bool   `mapstructure:"enable"`
	JSONLPath      string `mapstructure:"jsonl_path"`
	CaptureContent string `mapstructure:"capture_content"`
	PreviewChars   int    `mapstructure:"preview_chars"`
}

// GetMaxSteps returns the max tool call steps for the main agent
func (c *AgentConfig) GetMaxSteps() int {
	if c == nil {
		return defaultAgentMaxSteps
	}

	maxSteps := c.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultAgentMaxSteps
	}
	if c.usesTools() && maxSteps < minToolAgentMaxSteps {
		return minToolAgentMaxSteps
	}
	return maxSteps
}

func (c *SubAgentConfig) usesTools() bool {
	return c != nil && (len(c.Tools) > 0 || len(c.McpServers) > 0)
}

func (c *AgentConfig) usesTools() bool {
	if c == nil {
		return false
	}
	if len(c.Tools) > 0 || len(c.McpServers) > 0 || len(c.SubAgents) > 0 {
		return true
	}
	for _, skill := range c.Skills {
		if skill != nil && (len(skill.Tools) > 0 || len(skill.McpServers) > 0) {
			return true
		}
	}
	return false
}

// IsAgentEnabled returns true if chatv2 agent mode is enabled for this chat config
func (ccs *ChatConfigSingle) IsAgentEnabled() bool {
	return ccs.Agent != nil && ccs.Agent.Enable
}

// IsAgentV3Enabled reports whether this chat uses agent-v3 execution.
func (ccs *ChatConfigSingle) IsAgentV3Enabled() bool {
	if ccs == nil || !ccs.IsAgentEnabled() {
		return false
	}
	return ccs.Agent != nil && ccs.Agent.V3 && BotConfig != nil && BotConfig.AgentV3 != nil && BotConfig.AgentV3.Enable
}

// IsAgentV3RichEnabled reports whether rich Telegram delivery is enabled for agent-v3.
func (ccs *ChatConfigSingle) IsAgentV3RichEnabled() bool {
	return ccs != nil && ccs.IsAgentV3Enabled() && ccs.Agent != nil && ccs.Agent.Rich
}

// BuiltinInjectionEnabled reports whether built-in agent-v3 skills should be injected.
func (c *AgentV3SkillsConfig) BuiltinInjectionEnabled() bool {
	return c == nil || c.InjectBuiltin == nil || *c.InjectBuiltin
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

func (c *AgentV3Config) readConfig() {
	err := viper.UnmarshalKey("agent_v3", c, viper.DecodeHook(DispatchFor()))
	if err != nil {
		zap.L().Warn("cannot parse agent_v3 config", zap.Error(err))
	}
}

func (c *AgentV3Config) checkConfig() {
	if c == nil {
		return
	}
	if c.ContextCache.RawTurns <= 0 {
		c.ContextCache.RawTurns = 12
	}
	if c.ContextCache.SummaryTurns <= 0 {
		c.ContextCache.SummaryTurns = 80
	}
	if c.ContextCache.MaxSummaryTokens <= 0 {
		c.ContextCache.MaxSummaryTokens = 2000
	}
	if c.ContextCache.MaxRawTokens <= 0 {
		c.ContextCache.MaxRawTokens = 6000
	}
	if c.ContextCache.RedisTTL == "" {
		c.ContextCache.RedisTTL = agentV3DefaultContextCacheRedisTTL
	}
	if c.Memory.Scope == "" {
		c.Memory.Scope = agentV3DefaultScope
	}
	if c.Memory.Scope != agentV3DefaultScope {
		zap.L().Warn("unsupported agent_v3 memory scope, reset to group", zap.String("scope", c.Memory.Scope))
		c.Memory.Scope = agentV3DefaultScope
	}
	if c.Memory.AllowGlobal {
		zap.L().Warn("agent_v3 memory allow_global is not supported in v3 first release, reset to false")
		c.Memory.AllowGlobal = false
	}
	if c.Memory.SnapshotMaxTokens <= 0 {
		c.Memory.SnapshotMaxTokens = 2000
	}
	if c.Memory.WritePolicy == "" {
		c.Memory.WritePolicy = agentV3DefaultMemoryWritePolicy
	}
	if c.Memory.WritePolicy != agentV3DefaultMemoryWritePolicy {
		zap.L().Warn("unsupported agent_v3 memory write_policy, reset to explicit_or_admin", zap.String("write_policy", c.Memory.WritePolicy))
		c.Memory.WritePolicy = agentV3DefaultMemoryWritePolicy
	}
	if c.Runtime.Mode == "" {
		c.Runtime.Mode = agentV3DefaultRuntimeMode
	}
	if c.Runtime.Mode != agentV3DefaultRuntimeMode {
		zap.L().Warn("unsupported agent_v3 runtime mode, reset to remote_http", zap.String("mode", c.Runtime.Mode))
		c.Runtime.Mode = agentV3DefaultRuntimeMode
	}
	if c.Runtime.Endpoint == "" {
		c.Runtime.Endpoint = agentV3DefaultRuntimeEndpoint
	}
	if c.Runtime.NamespaceScope == "" {
		c.Runtime.NamespaceScope = agentV3DefaultScope
	}
	if c.Runtime.NamespaceScope != agentV3DefaultScope {
		zap.L().Warn("unsupported agent_v3 runtime namespace_scope, reset to group", zap.String("namespace_scope", c.Runtime.NamespaceScope))
		c.Runtime.NamespaceScope = agentV3DefaultScope
	}
	if c.Runtime.CommandTimeout == "" {
		c.Runtime.CommandTimeout = agentV3DefaultCommandTimeout
	}
	if c.Runtime.RequestTimeout == "" {
		c.Runtime.RequestTimeout = c.Runtime.CommandTimeout
	}
	if c.Runtime.MaxOutputChars <= 0 {
		c.Runtime.MaxOutputChars = 12000
	}
	if !sameStringSet(c.Tools.ExposeOnly, agentV3FixedTools) {
		if len(c.Tools.ExposeOnly) > 0 {
			zap.L().Warn("agent_v3 tools.expose_only must stay fixed to runtime tools, reset to default",
				zap.Strings("configured", c.Tools.ExposeOnly),
				zap.Strings("expected", agentV3FixedTools),
			)
		}
		c.Tools.ExposeOnly = append([]string(nil), agentV3FixedTools...)
	}
	if c.Skills.Mode == "" {
		c.Skills.Mode = agentV3DefaultSkillsMode
	}
	if c.Skills.Mode != agentV3DefaultSkillsMode {
		zap.L().Warn("unsupported agent_v3 skills mode, reset to system_prompt", zap.String("mode", c.Skills.Mode))
		c.Skills.Mode = agentV3DefaultSkillsMode
	}
	if c.Skills.InjectBuiltin == nil {
		injectBuiltin := true
		c.Skills.InjectBuiltin = &injectBuiltin
	}
	if c.Skills.SearXNG.Timeout == "" {
		c.Skills.SearXNG.Timeout = agentV3DefaultSearXNGTimeout
	}
	if c.Skills.SearXNG.MaxResponseBytes == 0 {
		c.Skills.SearXNG.MaxResponseBytes = agentV3DefaultSearXNGMaxBody
	}
	if c.Skills.SearXNG.MaxResults == 0 {
		c.Skills.SearXNG.MaxResults = agentV3DefaultSearXNGMaxResults
	}
	if c.Skills.SearXNG.MaxResultChars == 0 {
		c.Skills.SearXNG.MaxResultChars = agentV3DefaultSearXNGMaxResultChars
	}
	if c.Skills.SearXNG.DefaultLanguage == "" {
		c.Skills.SearXNG.DefaultLanguage = agentV3DefaultSearXNGLanguage
	}
	if c.Skills.SearXNG.DefaultResponseFormat == "" {
		c.Skills.SearXNG.DefaultResponseFormat = agentV3DefaultSearXNGFormat
	}
	if c.Skills.SearXNG.UserAgent == "" {
		c.Skills.SearXNG.UserAgent = agentV3DefaultSearXNGUserAgent
	}
	if c.Observability.JSONLPath == "" {
		c.Observability.JSONLPath = agentV3DefaultObservabilityJSONL
	}
	if c.Observability.CaptureContent == "" {
		c.Observability.CaptureContent = agentV3DefaultCaptureContent
	}
	if c.Observability.PreviewChars <= 0 {
		c.Observability.PreviewChars = 512
	}
}

// ContextCacheTTL returns the parsed agent-v3 context cache TTL.
func (c *AgentV3Config) ContextCacheTTL() time.Duration {
	if c == nil {
		return 30 * 24 * time.Hour
	}
	return parseFlexibleDuration(c.ContextCache.RedisTTL, 30*24*time.Hour)
}

// RuntimeCommandTimeout returns the agent-v3 runtime command timeout.
func (c *AgentV3Config) RuntimeCommandTimeout() time.Duration {
	if c == nil {
		return 120 * time.Second
	}
	return parseFlexibleDuration(c.Runtime.CommandTimeout, 120*time.Second)
}

// RuntimeRequestTimeout returns the agent-v3 runtime HTTP request timeout.
func (c *AgentV3Config) RuntimeRequestTimeout() time.Duration {
	if c == nil {
		return 120 * time.Second
	}
	return parseFlexibleDuration(c.Runtime.RequestTimeout, c.RuntimeCommandTimeout())
}

// RuntimeFetchEnabled reports whether controlled external fetch guidance is enabled.
func (c *AgentV3Config) RuntimeFetchEnabled() bool {
	return c != nil && c.Runtime.FetchEnabled != nil && *c.Runtime.FetchEnabled
}

// ValidateSearXNG validates the enabled SearXNG skill configuration.
func (c *AgentV3Config) ValidateSearXNG() error {
	if c == nil || !c.Skills.SearXNG.Enable {
		return nil
	}

	searxng := c.Skills.SearXNG
	parsedBaseURL, err := url.Parse(searxng.BaseURL)
	if err != nil || !parsedBaseURL.IsAbs() || parsedBaseURL.Opaque != "" || parsedBaseURL.Host == "" {
		return errInvalidAgentV3SearXNGBaseURL
	}
	if scheme := strings.ToLower(parsedBaseURL.Scheme); scheme != "http" && scheme != "https" {
		return errInvalidAgentV3SearXNGBaseURL
	}
	if parsedBaseURL.User != nil || parsedBaseURL.RawQuery != "" || parsedBaseURL.ForceQuery || parsedBaseURL.Fragment != "" {
		return errInvalidAgentV3SearXNGBaseURL
	}

	hasUsername := searxng.UsernameEnv != ""
	hasPassword := searxng.PasswordEnv != ""
	if hasUsername != hasPassword || (hasUsername && (!agentV3EnvironmentName.MatchString(searxng.UsernameEnv) || !agentV3EnvironmentName.MatchString(searxng.PasswordEnv))) {
		return errInvalidAgentV3SearXNGCredentialsEnvironment
	}

	timeout, err := time.ParseDuration(searxng.Timeout)
	if err != nil || timeout < time.Millisecond || timeout > 30*time.Second {
		return errInvalidAgentV3SearXNGTimeout
	}
	if searxng.MaxResponseBytes < 1 || searxng.MaxResponseBytes > 5*1024*1024 {
		return errInvalidAgentV3SearXNGMaxResponseBytes
	}
	if searxng.MaxResults < 1 || searxng.MaxResults > 20 {
		return errInvalidAgentV3SearXNGMaxResults
	}
	if searxng.MaxResultChars < 1 || searxng.MaxResultChars > 16384 || int64(searxng.MaxResultChars) > searxng.MaxResponseBytes {
		return errInvalidAgentV3SearXNGMaxResultChars
	}
	if utf8.RuneCountInString(searxng.DefaultLanguage) < 1 || utf8.RuneCountInString(searxng.DefaultLanguage) > 64 || containsControlCharacter(searxng.DefaultLanguage) {
		return errInvalidAgentV3SearXNGDefaultLanguage
	}
	if searxng.DefaultSafeSearch < 0 || searxng.DefaultSafeSearch > 2 {
		return errInvalidAgentV3SearXNGDefaultSafeSearch
	}
	if searxng.DefaultResponseFormat != "text" && searxng.DefaultResponseFormat != "json" {
		return errInvalidAgentV3SearXNGDefaultResponseFormat
	}
	if len(searxng.UserAgent) < 1 || len(searxng.UserAgent) > 512 || containsControlCharacter(searxng.UserAgent) {
		return errInvalidAgentV3SearXNGUserAgent
	}

	return nil
}

// SearXNGTimeout returns the parsed SearXNG request timeout.
func (c *AgentV3Config) SearXNGTimeout() time.Duration {
	if c == nil {
		return parseFlexibleDuration(agentV3DefaultSearXNGTimeout, 10*time.Second)
	}
	return parseFlexibleDuration(c.Skills.SearXNG.Timeout, 10*time.Second)
}

func containsControlCharacter(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// EffectiveModel returns the agent-v3 model override or the chat fallback.
func (c *AgentV3Config) EffectiveModel(fallback *Model) *Model {
	if c != nil && c.Model != nil {
		return c.Model
	}
	return fallback
}

func parseFlexibleDuration(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if strings.HasSuffix(raw, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	return fallback
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, item := range a {
		seen[item]++
	}
	for _, item := range b {
		seen[item]--
		if seen[item] < 0 {
			return false
		}
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}

// Tool server type constants define the supported connection protocol for tool servers.
const (
	ToolServerTypeSSE            = "sse"
	ToolServerTypeStreamableHTTP = "streamable-http"
	ToolServerTypeMCPO           = "mcpo"
)

// ToolServerConfig configures a tool server connection (MCP direct or MCPO proxy).
type ToolServerConfig struct {
	Enable bool        `mapstructure:"enable"`
	Type   string      `mapstructure:"type"`
	Url    string      `mapstructure:"url"`
	ApiKey string      `mapstructure:"api_key"`
	Tools  ToolEntries `mapstructure:"tools"`
}

// GetType returns the effective server type, defaulting to "sse".
func (c *ToolServerConfig) GetType() string {
	if c == nil || c.Type == "" {
		return ToolServerTypeSSE
	}
	t := strings.ToLower(strings.TrimSpace(c.Type))
	switch t {
	case ToolServerTypeSSE, ToolServerTypeStreamableHTTP, ToolServerTypeMCPO:
		return t
	default:
		return ToolServerTypeSSE
	}
}

// ToolEntry represents a tool name (MCP mode) or a toolset with optional sub-tool filter (MCPO mode).
type ToolEntry struct {
	Name  string
	Tools []string // nil = all tools in toolset
}

// ToolEntries supports union[string, map[toolset]([]string)] config format.
type ToolEntries []ToolEntry

var _ DispatchableType = ToolEntries(nil)

// From implements DispatchableType.
func (t ToolEntries) From(src reflect.Value) (any, error) {
	kind := src.Kind()
	for kind == reflect.Pointer || kind == reflect.Interface {
		if src.IsNil() {
			return nil, nil
		}
		src = src.Elem()
		kind = src.Kind()
	}

	switch kind {
	case reflect.Slice, reflect.Array:
		return parseToolEntriesSlice(src)
	default:
		return nil, ErrUnsupportedType
	}
}

func parseToolEntriesSlice(src reflect.Value) (ToolEntries, error) {
	var entries ToolEntries
	for i := range src.Len() {
		elem := src.Index(i)
		for elem.Kind() == reflect.Interface || elem.Kind() == reflect.Pointer {
			if elem.IsNil() {
				break
			}
			elem = elem.Elem()
		}

		switch elem.Kind() {
		case reflect.String:
			name := strings.TrimSpace(elem.String())
			if name != "" {
				entries = append(entries, ToolEntry{Name: name})
			}
		case reflect.Map:
			for _, key := range elem.MapKeys() {
				name := strings.TrimSpace(key.String())
				if name == "" {
					continue
				}
				val := elem.MapIndex(key)
				tools := extractStringSlice(val)
				entries = append(entries, ToolEntry{Name: name, Tools: tools})
			}
		default:
		}
	}
	return entries, nil
}

func extractStringSlice(v reflect.Value) []string {
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
		return nil
	}
	var result []string
	for i := range v.Len() {
		item := v.Index(i)
		for item.Kind() == reflect.Interface || item.Kind() == reflect.Pointer {
			if item.IsNil() {
				break
			}
			item = item.Elem()
		}
		if item.Kind() == reflect.String {
			s := strings.TrimSpace(item.String())
			if s != "" {
				result = append(result, s)
			}
		}
	}
	return result
}

// Names returns a flat list of all entry names (ignoring sub-tool filters).
func (t ToolEntries) Names() []string {
	if len(t) == 0 {
		return nil
	}
	names := make([]string, 0, len(t))
	for _, e := range t {
		names = append(names, e.Name)
	}
	return names
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
