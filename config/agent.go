package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-viper/mapstructure/v2"
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
	RequestTimeout       string `mapstructure:"request_timeout"`
	StreamIdleTimeout    string `mapstructure:"stream_idle_timeout"`

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

// RequestTimeoutDuration returns the per-HTTP-request timeout for model calls; 0 disables it.
func (m *Model) RequestTimeoutDuration() time.Duration {
	if m == nil {
		return 0
	}
	return max(parseFlexibleDuration(m.RequestTimeout, 0), 0)
}

// StreamIdleTimeoutDuration returns how long a model stream may stay silent before it is retried; 0 disables the watchdog.
func (m *Model) StreamIdleTimeoutDuration() time.Duration {
	if m == nil {
		return defaultModelStreamIdleTimeout
	}
	d := parseFlexibleDuration(m.StreamIdleTimeout, defaultModelStreamIdleTimeout)
	if d < 0 {
		return defaultModelStreamIdleTimeout
	}
	return d
}

// AgentTrigger is the configuration for an agent trigger.
type AgentTrigger struct {
	Command string `mapstructure:"command"`
	Regex   string `mapstructure:"regex"`
	Reply   bool   `mapstructure:"reply"`
	// Hint is appended to the end of the current user message when this trigger fires.
	Hint string `mapstructure:"hint"`
}

// AgentTriggerHintMaxChars bounds the per-trigger hint length.
const AgentTriggerHintMaxChars = 500

var errAgentTriggerHintTooLong = errors.New("agents[].trigger[].hint exceeds 500 characters")

// Validate rejects trigger hints that exceed the length bound.
func (t *AgentTrigger) Validate() error {
	if t == nil {
		return nil
	}
	if utf8.RuneCountInString(t.Hint) > AgentTriggerHintMaxChars {
		return fmt.Errorf("%w: %d characters", errAgentTriggerHintTooLong, utf8.RuneCountInString(t.Hint))
	}
	return nil
}

// ValidateTriggers checks every trigger of this agent.
func (ccs *AgentConfig) ValidateTriggers() error {
	if ccs == nil {
		return errAgentConfigNil
	}
	for i, trigger := range ccs.Trigger {
		if err := trigger.Validate(); err != nil {
			return fmt.Errorf("agent %q trigger[%d]: %w", ccs.Name, i, err)
		}
	}
	return nil
}

// ValidateSubAgents checks the subagent definitions of this agent.
func (ccs *AgentConfig) ValidateSubAgents() error {
	if ccs == nil {
		return errAgentConfigNil
	}
	if ccs.Agent == nil {
		return nil
	}
	for j, sub := range ccs.Agent.SubAgents {
		if sub == nil {
			return fmt.Errorf("agent %q: %w: agents[].agent.subagents[%d] is null", ccs.Name, errSubAgentConfigNull, j)
		}
		if err := sub.ValidateSkills(); err != nil {
			return fmt.Errorf("agent %q: %w", ccs.Name, err)
		}
	}
	return nil
}

// AgentOutputConfig is the configuration for tg message format
type AgentOutputConfig struct {
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

	defaultSubAgentMaxSteps               = 5
	defaultRuntimeSubAgentMaxSteps        = 8
	defaultSubAgentMaxResultChars         = 4000
	agentV3MemoryWritePolicyExplicitQuota = "explicit_quota"
	agentV3DefaultMemoryMaxEntriesPerUser = 20
	defaultAgentMaxSteps                  = 12
	defaultModelRetryNums                 = 3
	defaultModelRetryInitialInterval      = 500 * time.Millisecond
	defaultModelStreamIdleTimeout         = 60 * time.Second
	defaultAgentFinalReserve              = 90 * time.Second
	defaultAgentV3BusyMessage             = "当前任务太多，稍后再试。"
	minToolAgentMaxSteps                  = 4
	agentV3DefaultScope                   = "group"
	agentV3DefaultMemoryWritePolicy       = "explicit_or_admin"
	agentV3DefaultRuntimeMode             = "remote_http"
	agentV3DefaultSkillsMode              = "system_prompt"
	agentV3DefaultRuntimeEndpoint         = "http://agent-runtime:8080"
	agentV3DefaultCommandTimeout          = "120s"
	agentV3DefaultObservabilityJSONL      = "logs/agentv3-traces.jsonl"
	agentV3DefaultTraceMaxSizeMB          = 50
	agentV3DefaultTraceMaxBackups         = 5
	agentV3DefaultTraceQueue              = 256
	agentV3DefaultShutdownGrace           = 60 * time.Second
	agentV3DefaultCaptureContent          = "preview"
	agentV3DefaultContextCacheRedisTTL    = "30d"
	agentV3DefaultSessionDirectory        = "data/agent-sessions"
	agentV3DefaultSessionTTL              = "24h"
	agentV3DefaultSessionOverflowStrategy = "rebuild"
	agentV3DefaultSearXNGTimeout          = "10s"
	agentV3DefaultSearXNGMaxBody          = int64(1024 * 1024)
	agentV3DefaultSearXNGMaxResults       = 10
	agentV3DefaultSearXNGMaxResultChars   = 2000
	agentV3DefaultSearXNGLanguage         = "zh-CN"
	agentV3DefaultSearXNGFormat           = "text"
	agentV3DefaultSearXNGUserAgent        = "csust-got-agent-v3"
)

var agentV3FixedTools = []string{"read", "grep", "write", "edit", "bash"}

var (
	errAgentConfigNil                        = errors.New("agent config is nil")
	errAgentContextModeUnsupported           = errors.New("unsupported context_mode")
	errInvalidAgentV3SessionTTL              = errors.New("invalid agent_v3.session.ttl: must be a positive Go duration")
	errInvalidAgentV3SessionEnable           = errors.New("invalid agent_v3.session.enable: must be a boolean")
	errInvalidAgentV3SessionType             = errors.New("invalid agent_v3.session: must be an object with a string directory")
	errInvalidAgentV3SessionOverflow         = errors.New("invalid agent_v3.session.context_overflow: strategy must be rebuild and max_tokens must be a positive int64")
	errInvalidAgentSessionOverflow           = errors.New("invalid agents[].session.context_overflow: max_tokens must be a positive int64")
	errInvalidAgentV3SessionOverflowPatterns = errors.New("invalid agent_v3.session.context_overflow.patterns: every pattern must be a valid regular expression")
	errInvalidAgentV3SessionCompact          = errors.New("invalid agent_v3.session.compact: enable must be a boolean, threshold_tokens/keep_recent_turns/summary_max_chars must be positive integers, and model must be an object with a non-empty model name")
)

var agentV3EnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var agentV3SkillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

var errInvalidSubAgentSkillName = errors.New("invalid agents[].agent.subagents[].skills entry")

var errSubAgentConfigNull = errors.New("invalid agents[].agent.subagents entry")

var (
	errRuntimeEnvLimit    = errors.New("runtime_env_limit")
	errRuntimeEnvName     = errors.New("runtime_env_name")
	errRuntimeEnvReserved = errors.New("runtime_env_reserved")
	errRuntimeEnvValue    = errors.New("runtime_env_value")
)

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
func (c *AgentOutputConfig) GetFormat() string {
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
func (c *AgentOutputConfig) GetReasonFormat() string {
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
func (c *AgentOutputConfig) GetPayloadFormat() string {
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
func (c *AgentOutputConfig) GetEditInterval() time.Duration {
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
func (c *AgentOutputConfig) GetUseNativeReasoning() bool {
	if c.UseNativeReasoning == nil {
		return true // default to using native reasoning
	}
	return *c.UseNativeReasoning
}

// AgentFilterConfig represents the configuration for a filter
type AgentFilterConfig struct {
	// Type is the type of filter (e.g., "whitelist")
	Type string `mapstructure:"type"`

	// Whitelist filter configuration
	Whitelist []int64 `mapstructure:"whitelist,omitempty"`
}

// AgentFilterSettings represents the filter settings for an agent configuration.
type AgentFilterSettings struct {
	// Filters is a list of filters to apply in order
	Filters []AgentFilterConfig `mapstructure:"filters"`
}

// AgentV3Configs is the configured set of agents.
type AgentV3Configs []*AgentConfig

// AgentConfig is the configuration for a single configured agent.
type AgentConfig struct {
	Name            string             `mapstructure:"name"`
	Model           *Model             `mapstructure:"model"`
	MessageContext  int                `mapstructure:"message_context"`
	ContextMode     string             `mapstructure:"context_mode"`
	Temperature     *float32           `mapstructure:"temperature"`
	PlaceHolder     string             `mapstructure:"place_holder"`
	ErrorMessage    string             `mapstructure:"error_message"` // 添加错误提示消息配置
	SystemPrompt    JoinableString     `mapstructure:"system_prompt"`
	PromptTemplate  JoinableString     `mapstructure:"prompt_template"`
	Trigger         []*AgentTrigger    `mapstructure:"trigger"`
	Timeout         int                `mapstructure:"timeout"` // seconds
	Format          AgentOutputConfig  `mapstructure:"format"`
	ReasoningEffort string             `mapstructure:"reasoning_effort"`
	Session         AgentSessionConfig `mapstructure:"session"`

	Agent    *AgentOptions       `mapstructure:"agent"`
	Features FeatureSetting      `mapstructure:"features"`
	Filters  AgentFilterSettings `mapstructure:"filters"`
}

// AgentSessionConfig controls per-agent DAG context saving and loading.
type AgentSessionConfig struct {
	SaveContext     *bool                             `mapstructure:"save_context"`
	LoadContext     bool                              `mapstructure:"load_context"`
	ContextOverflow AgentSessionContextOverflowConfig `mapstructure:"context_overflow"`
	decodeErr       error
}

// AgentSessionContextOverflowConfig optionally overrides the global session input limit.
type AgentSessionContextOverflowConfig struct {
	MaxTokens *int64 `mapstructure:"max_tokens"`
}

// From decodes per-agent session settings without weakening the token limit schema.
func (c AgentSessionConfig) From(src reflect.Value) (any, error) {
	if src.Type() == reflect.TypeFor[AgentSessionConfig]() {
		return src.Interface(), nil
	}
	var out AgentSessionConfig
	if err := decodeAgentSessionConfig(src.Interface(), &out); err != nil {
		out.decodeErr = errInvalidAgentSessionOverflow
	}
	return out, nil
}

// Validate rejects explicit invalid limits even when saving and loading are disabled.
func (c AgentSessionConfig) Validate() error {
	if c.decodeErr != nil || (c.ContextOverflow.MaxTokens != nil && *c.ContextOverflow.MaxTokens <= 0) {
		return errInvalidAgentSessionOverflow
	}
	return nil
}

// EffectiveSessionTokenLimit inherits the global input limit unless the agent overrides it.
func (ccs *AgentConfig) EffectiveSessionTokenLimit(global AgentV3SessionConfig) int64 {
	if ccs != nil && ccs.Session.ContextOverflow.MaxTokens != nil {
		return *ccs.Session.ContextOverflow.MaxTokens
	}
	return global.ContextOverflow.TokenLimit()
}

// SaveEnabled reports whether context saving is enabled, defaulting to true.
func (c AgentSessionConfig) SaveEnabled() bool {
	return c.SaveContext == nil || *c.SaveContext
}

// LoadEnabled reports whether context loading is enabled, defaulting to false.
func (c AgentSessionConfig) LoadEnabled() bool {
	return c.LoadContext
}

// EffectiveSessionSettings returns session switches, forcing both on for reply invocations.
func (ccs *AgentConfig) EffectiveSessionSettings(trigger *AgentTrigger) (save, load bool) {
	if trigger != nil && trigger.Reply {
		return true, true
	}
	if ccs == nil {
		return true, false
	}
	return ccs.Session.SaveEnabled(), ccs.Session.LoadEnabled()
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
	// Runtime grants the subagent the same remote Runtime tools (read/grep/write/edit/bash) as the main agent.
	Runtime bool `mapstructure:"runtime"`
	// Skills lists the agent-v3 skill names the subagent may load through load_skill.
	Skills []string `mapstructure:"skills"`
	// MaxResultChars caps the text returned to the main agent; head and tail are kept.
	MaxResultChars int `mapstructure:"max_result_chars"`
}

// GetMaxSteps returns the max tool call steps for the subagent
func (c *SubAgentConfig) GetMaxSteps() int {
	if c == nil {
		return defaultSubAgentMaxSteps
	}

	maxSteps := c.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultSubAgentMaxSteps
		if c.Runtime {
			maxSteps = defaultRuntimeSubAgentMaxSteps
		}
	}
	if c.usesTools() && maxSteps < minToolAgentMaxSteps {
		return minToolAgentMaxSteps
	}
	return maxSteps
}

// GetMaxResultChars returns the result cap for the subagent, defaulting to 4000.
func (c *SubAgentConfig) GetMaxResultChars() int {
	if c == nil || c.MaxResultChars <= 0 {
		return defaultSubAgentMaxResultChars
	}
	return c.MaxResultChars
}

// ValidateSkills rejects skill names that are not canonical agent-v3 skill names.
func (c *SubAgentConfig) ValidateSkills() error {
	if c == nil {
		return nil
	}
	for _, name := range c.Skills {
		if !agentV3SkillName.MatchString(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")) {
			return fmt.Errorf("%w: subagent %q skill %q", errInvalidSubAgentSkillName, c.Name, name)
		}
	}
	return nil
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

// AgentOptions defines the execution options for an agent.
type AgentOptions struct {
	Enable     bool                `mapstructure:"enable"`
	Rich       bool                `mapstructure:"rich"`
	Tools      []string            `mapstructure:"tools"`
	MaxSteps   int                 `mapstructure:"max_steps"`
	SubAgents  []*SubAgentConfig   `mapstructure:"subagents"`
	McpServers []*ToolServerConfig `mapstructure:"mcp_servers"`
	ToolModels map[string]*Model   `mapstructure:"tool_models"`
	Skills     []*SkillConfig      `mapstructure:"skills"`
	// FinalReserve is the time kept before the turn deadline for one tool-free final model call.
	FinalReserve string `mapstructure:"final_reserve"`
}

// AgentV3Config defines global agent-v3 defaults and runtime settings.
type AgentV3Config struct {
	Enable        bool                       `mapstructure:"enable"`
	Model         *Model                     `mapstructure:"model"`
	SoulPath      string                     `mapstructure:"soul_path"`
	Cron          AgentV3CronConfig          `mapstructure:"cron"`
	ContextCache  AgentV3ContextCacheConfig  `mapstructure:"context_cache"`
	Session       AgentV3SessionConfig       `mapstructure:"session"`
	Memory        AgentV3MemoryConfig        `mapstructure:"memory"`
	Runtime       AgentV3RuntimeConfig       `mapstructure:"runtime"`
	Tools         AgentV3ToolsConfig         `mapstructure:"tools"`
	Skills        AgentV3SkillsConfig        `mapstructure:"skills"`
	Observability AgentV3ObservabilityConfig `mapstructure:"observability"`
	ShutdownGrace string                     `mapstructure:"shutdown_grace"`
	Concurrency   AgentV3ConcurrencyConfig   `mapstructure:"concurrency"`
}

// AgentV3ConcurrencyConfig bounds concurrent agent runs and model calls process-wide; 0 means unlimited.
type AgentV3ConcurrencyConfig struct {
	MaxRuns       int    `mapstructure:"max_runs"`
	MaxModelCalls int    `mapstructure:"max_model_calls"`
	BusyMessage   string `mapstructure:"busy_message"`
}

// GetBusyMessage returns the reply sent when the run limit is reached.
func (c AgentV3ConcurrencyConfig) GetBusyMessage() string {
	if msg := strings.TrimSpace(c.BusyMessage); msg != "" {
		return msg
	}
	return defaultAgentV3BusyMessage
}

// AgentV3SessionConfig controls shared archives, idle TTL and restored-input acceptance.
type AgentV3SessionConfig struct {
	Enable          *bool                               `mapstructure:"enable"`
	Directory       string                              `mapstructure:"directory"`
	TTL             string                              `mapstructure:"ttl"`
	ContextOverflow AgentV3SessionContextOverflowConfig `mapstructure:"context_overflow"`
	Compact         AgentV3SessionCompactConfig         `mapstructure:"compact"`

	decodeErr   error
	invalidType bool
}

// AgentV3SessionCompactConfig controls background summarization of long DAGs into a new root.
type AgentV3SessionCompactConfig struct {
	Enable          bool   `mapstructure:"enable"`
	ThresholdTokens int64  `mapstructure:"threshold_tokens"`
	KeepRecentTurns int64  `mapstructure:"keep_recent_turns"`
	SummaryMaxChars int64  `mapstructure:"summary_max_chars"`
	Model           *Model `mapstructure:"model"`
}

// Threshold returns the configured trigger, defaulting to 60% of the effective input limit.
func (c AgentV3SessionCompactConfig) Threshold(limit int64) int64 {
	if c.ThresholdTokens > 0 {
		return c.ThresholdTokens
	}
	return max(limit*6/10, 1)
}

// RecentTurns returns how many latest turns replay verbatim after the summary, defaulting to 2.
func (c AgentV3SessionCompactConfig) RecentTurns() int {
	if c.KeepRecentTurns > 0 {
		return int(c.KeepRecentTurns)
	}
	return 2
}

// MaxSummaryChars bounds the summary text in runes, defaulting to 6000.
func (c AgentV3SessionCompactConfig) MaxSummaryChars() int {
	if c.SummaryMaxChars > 0 {
		return int(c.SummaryMaxChars)
	}
	return 6000
}

// Validate rejects a configured summarizer model without a model name; omitted values use defaults.
func (c AgentV3SessionCompactConfig) Validate() error {
	if c.ThresholdTokens < 0 || c.KeepRecentTurns < 0 || c.SummaryMaxChars < 0 || c.Model != nil && c.Model.Model == "" {
		return errInvalidAgentV3SessionCompact
	}
	return nil
}

// AgentV3SessionContextOverflowConfig controls acceptance of restored model input.
type AgentV3SessionContextOverflowConfig struct {
	Strategy  string   `mapstructure:"strategy"`
	MaxTokens int64    `mapstructure:"max_tokens"`
	Patterns  []string `mapstructure:"patterns"`
}

// ValidatePatterns rejects provider context-limit patterns that do not compile.
func (c AgentV3SessionContextOverflowConfig) ValidatePatterns() error {
	for _, pattern := range c.Patterns {
		if _, err := regexp.Compile("(?is)(?:" + pattern + ")"); err != nil {
			return fmt.Errorf("%w: %q", errInvalidAgentV3SessionOverflowPatterns, pattern)
		}
	}
	return nil
}

// Enabled defaults to true and overrides per-agent reply settings when false.
func (c AgentV3SessionConfig) Enabled() bool { return c.Enable == nil || *c.Enable }

// StrategyName returns the configured acceptance strategy, defaulting to rebuild.
func (c AgentV3SessionContextOverflowConfig) StrategyName() string {
	if c.Strategy == "" {
		return agentV3DefaultSessionOverflowStrategy
	}
	return c.Strategy
}

// TokenLimit defaults to 200000 only when the field was omitted.
func (c AgentV3SessionContextOverflowConfig) TokenLimit() int64 {
	if c.MaxTokens == 0 {
		return 200000
	}
	return c.MaxTokens
}

// From preserves invalid explicit values for Validate rather than weak-decoding them.
func (c AgentV3SessionConfig) From(src reflect.Value) (any, error) {
	if src.Type() == reflect.TypeFor[AgentV3SessionConfig]() {
		return src.Interface(), nil
	}
	out := AgentV3SessionConfig{ContextOverflow: AgentV3SessionContextOverflowConfig{Strategy: agentV3DefaultSessionOverflowStrategy, MaxTokens: 200000}}
	err := decodeAgentSessionConfig(src.Interface(), &out)
	if err != nil {
		out.decodeErr = errInvalidAgentV3SessionType
		var fieldErr *mapstructure.DecodeError
		if errors.As(err, &fieldErr) {
			switch {
			case fieldErr.Name() == "ttl":
				out.decodeErr = errInvalidAgentV3SessionTTL
			case fieldErr.Name() == "enable":
				out.decodeErr = errInvalidAgentV3SessionEnable
			case strings.HasPrefix(fieldErr.Name(), "context_overflow"):
				out.decodeErr = errInvalidAgentV3SessionOverflow
			case strings.HasPrefix(fieldErr.Name(), "compact"):
				out.decodeErr = errInvalidAgentV3SessionCompact
			}
		}
	}
	if out.ContextOverflow.Strategy == "" {
		out.decodeErr = errInvalidAgentV3SessionOverflow
	}
	return out, nil
}

func decodeAgentSessionConfig(raw, out any) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result: out, DecodeNil: true,
		DecodeHook: mapstructure.DecodeHookFuncValue(strictAgentSessionValue),
	})
	if err != nil {
		return err
	}
	return decoder.Decode(raw)
}

func strictAgentSessionValue(from, to reflect.Value) (any, error) {
	if !from.IsValid() || (from.Kind() == reflect.Pointer && from.IsNil()) {
		return nil, ErrUnsupportedType
	}
	target := to.Type()
	if target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		if target == reflect.TypeFor[Model]() {
			return decodeAgentSessionModel(from)
		}
		fields, ok := agentV3SessionConfigMap(from.Interface())
		if !ok {
			return nil, ErrUnsupportedType
		}
		return fields, nil
	case reflect.String:
		if from.Kind() != reflect.String {
			return nil, ErrUnsupportedType
		}
	case reflect.Bool:
		if from.Kind() == reflect.String {
			return strconv.ParseBool(from.String())
		}
		if from.Kind() != reflect.Bool {
			return nil, ErrUnsupportedType
		}
	case reflect.Int64:
		switch {
		case from.Kind() == reflect.String:
			value, err := strconv.ParseInt(from.String(), 10, 64)
			if err == nil && value > 0 {
				return value, nil
			}
		case from.CanInt() && from.Int() > 0:
			return from.Int(), nil
		case from.CanUint() && from.Uint() > 0 && from.Uint() <= math.MaxInt64:
			return int64(from.Uint()), nil
		}
		return nil, ErrUnsupportedType
	case reflect.Slice:
		if target.Elem().Kind() != reflect.String {
			return nil, ErrUnsupportedType
		}
		return agentSessionStringList(from)
	default:
		return nil, ErrUnsupportedType
	}
	return from.Interface(), nil
}

// decodeAgentSessionModel decodes a nested model object strictly, keeping the Model schema intact.
func decodeAgentSessionModel(from reflect.Value) (any, error) {
	if from.Kind() == reflect.Interface {
		from = from.Elem()
	}
	if from.IsValid() && from.Type() == reflect.TypeFor[Model]() {
		return from.Interface(), nil
	}
	fields, ok := agentV3SessionConfigMap(from.Interface())
	if !ok {
		return nil, ErrUnsupportedType
	}
	var out Model
	if err := mapstructure.Decode(fields, &out); err != nil {
		return nil, ErrUnsupportedType
	}
	return out, nil
}

func agentSessionStringList(from reflect.Value) (any, error) {
	if from.Kind() == reflect.Interface {
		from = from.Elem()
	}
	if from.Kind() == reflect.String {
		return []string{from.String()}, nil
	}
	if from.Kind() != reflect.Slice && from.Kind() != reflect.Array {
		return nil, ErrUnsupportedType
	}
	out := make([]string, 0, from.Len())
	for i := range from.Len() {
		item := from.Index(i)
		for item.Kind() == reflect.Interface && !item.IsNil() {
			item = item.Elem()
		}
		if item.Kind() != reflect.String {
			return nil, ErrUnsupportedType
		}
		out = append(out, item.String())
	}
	return out, nil
}

func agentV3SessionConfigMap(raw any) (map[string]any, bool) {
	v := reflect.ValueOf(raw)
	if !v.IsValid() || v.Kind() != reflect.Map || v.IsNil() {
		return nil, false
	}
	fields := make(map[string]any, v.Len())
	for _, key := range v.MapKeys() {
		name, ok := key.Interface().(string)
		if !ok {
			return nil, false
		}
		value := v.MapIndex(key).Interface()
		if value == nil {
			value = struct{}{}
		}
		fields[strings.ToLower(name)] = value
	}
	return fields, true
}

// DirectoryPath returns the archive directory, defaulting to data/agent-sessions.
func (c AgentV3SessionConfig) DirectoryPath() string {
	if c.Directory == "" {
		return agentV3DefaultSessionDirectory
	}
	return c.Directory
}

// IdleTTL assumes the configuration has passed Validate.
func (c AgentV3SessionConfig) IdleTTL() time.Duration {
	if c.TTL == "" {
		return 24 * time.Hour
	}
	ttl, _ := time.ParseDuration(c.TTL)
	return ttl
}

// Validate checks explicit session settings even when session storage is disabled.
func (c AgentV3SessionConfig) Validate() error {
	if c.invalidType {
		return errInvalidAgentV3SessionType
	}
	if c.decodeErr != nil {
		return c.decodeErr
	}
	if c.ContextOverflow.StrategyName() != agentV3DefaultSessionOverflowStrategy || c.ContextOverflow.TokenLimit() <= 0 {
		return errInvalidAgentV3SessionOverflow
	}
	if err := c.ContextOverflow.ValidatePatterns(); err != nil {
		return err
	}
	if err := c.Compact.Validate(); err != nil {
		return err
	}
	if c.TTL == "" {
		return nil
	}
	ttl, err := time.ParseDuration(c.TTL)
	if err != nil || ttl <= 0 {
		return errInvalidAgentV3SessionTTL
	}
	return nil
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
	MaxEntriesPerUser int    `mapstructure:"max_entries_per_user"`
}

// QuotaWrites reports whether non-admin users may write memory within a per-user quota.
func (c AgentV3MemoryConfig) QuotaWrites() bool {
	return c.WritePolicy == agentV3MemoryWritePolicyExplicitQuota
}

// EffectiveMaxEntriesPerUser returns the per-user quota, defaulting to 20.
func (c AgentV3MemoryConfig) EffectiveMaxEntriesPerUser() int {
	if c.MaxEntriesPerUser <= 0 {
		return agentV3DefaultMemoryMaxEntriesPerUser
	}
	return c.MaxEntriesPerUser
}

// AgentV3RuntimeConfig points agent-v3 tools at the remote runtime service.
type AgentV3RuntimeConfig struct {
	Enable         bool              `mapstructure:"enable"`
	Mode           string            `mapstructure:"mode"`
	Endpoint       string            `mapstructure:"endpoint"`
	AuthTokenEnv   string            `mapstructure:"auth_token_env"`
	Env            map[string]string `mapstructure:"env"`
	NamespaceScope string            `mapstructure:"namespace_scope"`
	CommandTimeout string            `mapstructure:"command_timeout"`
	MaxOutputChars int               `mapstructure:"max_output_chars"`
	RequestTimeout string            `mapstructure:"request_timeout"`
	FetchEnabled   *bool             `mapstructure:"fetch_enabled"`
}

// ValidateAgentV3RuntimeEnv checks literal runtime environment entries against the runtime's limits.
func ValidateAgentV3RuntimeEnv(env map[string]string) error {
	if len(env) > 64 {
		return errRuntimeEnvLimit
	}

	serializedBytes := 2 // The outer JSON array.
	first := true
	for name, value := range env {
		if len(name) < 1 || len(name) > 128 || !agentV3EnvironmentName.MatchString(name) {
			return errRuntimeEnvName
		}
		upper := strings.ToUpper(name)
		switch upper {
		case "PATH", "HOME", "SHELL", "ENV", "BASH_ENV", "SHELLOPTS", "BASHOPTS", "IFS",
			"CDPATH", "PWD", "OLDPWD", "SHLVL", "PS4", "PROMPT_COMMAND", "GLOBIGNORE",
			"TMPDIR", "TMP", "TEMP", "GLIBC_TUNABLES", "GCONV_PATH", "LOCPATH", "NLSPATH",
			"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			return errRuntimeEnvReserved
		}
		for _, prefix := range [...]string{
			"AGENT_RUNTIME_", "AGENT_FETCH_", "LD_", "DYLD_", "BASH_", "PROOT_", "MALLOC_",
			"GIT_", "PYTHON", "PERL", "RUBY", "NODE_", "JAVA_", "JDK_", "_JAVA_", "SSL_",
			"CURL_", "WGET_",
		} {
			if strings.HasPrefix(upper, prefix) {
				return errRuntimeEnvReserved
			}
		}
		if len(value) > 2048 || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return errRuntimeEnvValue
		}
		if !first {
			serializedBytes++ // Comma between pairs.
		}
		first = false
		serializedBytes += 1 + len(name) + 2 + 1 + agentV3RuntimeJSONStringBytes(value) + 1 // ["name","value"]
	}
	if serializedBytes > 8192 {
		return errRuntimeEnvLimit
	}
	return nil
}

// ValidateAgentV3RuntimeEnvLayers checks visible layers before overrides, as the Runtime does.
func ValidateAgentV3RuntimeEnvLayers(layers ...map[string]string) error {
	count, serializedBytes := 0, 2
	for _, layer := range layers {
		if err := ValidateAgentV3RuntimeEnv(layer); err != nil {
			return err
		}
		for name, value := range layer {
			if count != 0 {
				serializedBytes++
			}
			count++
			if count > 64 {
				return errRuntimeEnvLimit
			}
			serializedBytes += 1 + len(name) + 2 + 1 + agentV3RuntimeJSONStringBytes(value) + 1
			if serializedBytes > 8192 {
				return errRuntimeEnvLimit
			}
		}
	}
	return nil
}

func agentV3RuntimeJSONStringBytes(value string) int {
	size := 2 // Surrounding quotes.
	for _, r := range value {
		switch r {
		case '"', '\\', '\b', '\f', '\n', '\r', '\t':
			size += 2
		default:
			if r < 0x20 {
				size += 6 // \u00XX
			} else {
				size += utf8.RuneLen(r)
			}
		}
	}
	return size
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
	Enable          bool   `mapstructure:"enable"`
	JSONLPath       string `mapstructure:"jsonl_path"`
	CaptureContent  string `mapstructure:"capture_content"`
	PreviewChars    int    `mapstructure:"preview_chars"`
	TraceMaxSizeMB  int    `mapstructure:"trace_max_size_mb"`
	TraceMaxBackups int    `mapstructure:"trace_max_backups"`
	TraceQueue      int    `mapstructure:"trace_queue"`
}

// GetMaxSteps returns the max tool call steps for the main agent
func (c *AgentOptions) GetMaxSteps() int {
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

// GetFinalReserve returns the deadline reserve kept for the tool-free final model call.
func (c *AgentOptions) GetFinalReserve() time.Duration {
	if c == nil {
		return defaultAgentFinalReserve
	}
	d := parseFlexibleDuration(c.FinalReserve, defaultAgentFinalReserve)
	if d < 0 {
		return defaultAgentFinalReserve
	}
	return d
}

func (c *SubAgentConfig) usesTools() bool {
	return c != nil && (len(c.Tools) > 0 || len(c.McpServers) > 0 || c.Runtime || len(c.Skills) > 0)
}

func (c *AgentOptions) usesTools() bool {
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

// IsAgentV3Enabled reports whether an agent can use agent-v3 execution.
func (ccs *AgentConfig) IsAgentV3Enabled() bool {
	if ccs == nil || ccs.Agent == nil || !ccs.Agent.Enable {
		return false
	}
	return BotConfig != nil && BotConfig.AgentV3 != nil && BotConfig.AgentV3.Enable
}

// UsesReplyChain reports whether this agent builds context from its reply chain.
func (ccs *AgentConfig) UsesReplyChain() bool {
	return ccs != nil && ccs.ContextMode == "reply_chain"
}

// ValidateContextMode rejects an unsupported context source without rewriting it.
func (ccs *AgentConfig) ValidateContextMode() error {
	if ccs == nil {
		return errAgentConfigNil
	}
	switch ccs.ContextMode {
	case "", "chat", "reply_chain":
		return nil
	default:
		return fmt.Errorf("agent %q: %w %q; use chat or reply_chain", ccs.Name, errAgentContextModeUnsupported, ccs.ContextMode)
	}
}

// IsAgentV3RichEnabled reports whether rich Telegram delivery is enabled for agent-v3.
func (ccs *AgentConfig) IsAgentV3RichEnabled() bool {
	return ccs != nil && ccs.IsAgentV3Enabled() && ccs.Agent != nil && ccs.Agent.Rich
}

// BuiltinInjectionEnabled reports whether built-in agent-v3 skills should be injected.
func (c *AgentV3SkillsConfig) BuiltinInjectionEnabled() bool {
	return c == nil || c.InjectBuiltin == nil || *c.InjectBuiltin
}

// TriggerOnReply checks if the agent will trigger on reply.
func (ccs *AgentConfig) TriggerOnReply() (*AgentTrigger, bool) {
	for _, t := range ccs.Trigger {
		if t.Reply {
			return t, true
		}
	}
	return nil, false
}

// GetTimeout returns the timeout for the agent model.
func (ccs *AgentConfig) GetTimeout() time.Duration {
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

func (c *AgentV3Config) readConfig() {
	if errRuntimeEnvConfigState != nil {
		return
	}
	invalidShape := invalidAgentV3SessionShape()
	err := viper.UnmarshalKey("agent_v3", c, viper.DecodeHook(DispatchFor()))
	if err != nil {
		zap.L().Warn("cannot parse agent_v3 config")
	}
	raw := agentSessionEnvironment(viper.Get("agent_v3.session"), reflect.TypeFor[AgentV3SessionConfig](), "agent_v3.session")
	decoded, _ := c.Session.From(reflect.ValueOf(raw))
	c.Session = decoded.(AgentV3SessionConfig)
	c.Session.invalidType = invalidShape
	c.Runtime.Env = make(map[string]string, len(runtimeEnvConfig))
	for name, value := range runtimeEnvConfig {
		c.Runtime.Env[name] = value
	}
}

// AutomaticEnv does not enumerate absent YAML keys; derive them from the same typed schema.
func agentSessionEnvironment(raw any, schema reflect.Type, path string) any {
	fields, ok := agentV3SessionConfigMap(raw)
	if !ok {
		if raw != nil {
			return raw
		}
		fields = make(map[string]any)
	}
	for i := range schema.NumField() {
		field := schema.Field(i)
		name := field.Tag.Get("mapstructure")
		if name == "" {
			continue
		}
		key := path + "." + name
		if field.Type.Kind() == reflect.Struct {
			value, exists := fields[name]
			if exists {
				if _, object := agentV3SessionConfigMap(value); !object {
					continue
				}
			}
			child := agentSessionEnvironment(value, field.Type, key).(map[string]any)
			if exists || len(child) > 0 {
				fields[name] = child
			}
		} else if viper.IsSet(key) {
			fields[name] = viper.Get(key)
		}
	}
	return fields
}

func invalidAgentV3SessionShape() bool {
	raw := viper.Get("agent_v3")
	parent, object := agentV3SessionConfigMap(raw)
	if !object {
		return raw != nil || slices.Contains(viper.AllKeys(), "agent_v3")
	}
	if rawSession, present := parent["session"]; present {
		_, object = agentV3SessionConfigMap(rawSession)
		return !object
	}
	return false
}

func (c *AgentV3Config) checkConfig() {
	if c == nil {
		return
	}
	if err := c.Session.Validate(); err != nil {
		zap.L().Panic("invalid agent_v3 session config", zap.Error(err))
	}
	if c.ShutdownGraceDuration() < 0 {
		zap.L().Panic("invalid agent_v3 shutdown_grace", zap.String("shutdown_grace", c.ShutdownGrace))
	}
	if c.Session.Directory == "" {
		c.Session.Directory = agentV3DefaultSessionDirectory
	}
	if c.Session.TTL == "" {
		c.Session.TTL = agentV3DefaultSessionTTL
	}
	c.Session.ContextOverflow.Strategy = c.Session.ContextOverflow.StrategyName()
	c.Session.ContextOverflow.MaxTokens = c.Session.ContextOverflow.TokenLimit()
	if c.ContextCache.RawTurns <= 0 {
		c.ContextCache.RawTurns = 12
	}
	c.Cron = c.Cron.WithDefaults()
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
	if c.Memory.WritePolicy != agentV3DefaultMemoryWritePolicy && c.Memory.WritePolicy != agentV3MemoryWritePolicyExplicitQuota {
		zap.L().Warn("unsupported agent_v3 memory write_policy, reset to explicit_or_admin", zap.String("write_policy", c.Memory.WritePolicy))
		c.Memory.WritePolicy = agentV3DefaultMemoryWritePolicy
	}
	if c.Memory.MaxEntriesPerUser <= 0 {
		c.Memory.MaxEntriesPerUser = agentV3DefaultMemoryMaxEntriesPerUser
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
	if c.Observability.TraceMaxSizeMB <= 0 {
		c.Observability.TraceMaxSizeMB = agentV3DefaultTraceMaxSizeMB
	}
	if c.Observability.TraceMaxBackups <= 0 {
		c.Observability.TraceMaxBackups = agentV3DefaultTraceMaxBackups
	}
	if c.Observability.TraceQueue <= 0 {
		c.Observability.TraceQueue = agentV3DefaultTraceQueue
	}
}

// ShutdownGraceDuration returns how long shutdown waits for in-flight agent turns.
func (c *AgentV3Config) ShutdownGraceDuration() time.Duration {
	if c == nil {
		return agentV3DefaultShutdownGrace
	}
	return parseFlexibleDuration(c.ShutdownGrace, agentV3DefaultShutdownGrace)
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

// GetTemperature returns the temperature for the agent model.
func (ccs *AgentConfig) GetTemperature() float32 {
	if ccs.Temperature != nil {
		return *ccs.Temperature
	}
	return 1.0
}

// GetErrorMessage returns the error message for the chat model
func (ccs *AgentConfig) GetErrorMessage() string {
	if ccs.ErrorMessage != "" {
		return ccs.ErrorMessage
	}
	return "😔很抱歉，我无法处理您的请求"
}

func (c *AgentV3Configs) readConfig() {
	v := viper.GetViper()
	err := v.UnmarshalKey("agents", c, viper.DecodeHook(DispatchFor()))
	for _, cfg := range *c {
		if cfg != nil {
			if sessionErr := cfg.Session.Validate(); sessionErr != nil {
				zap.L().Panic("invalid agent session config", zap.Error(sessionErr))
			}
		}
	}
	if err != nil {
		zap.L().Warn("cannot parse agents config", zap.Error(err))
		return
	}
	// An omitted agent section uses the default enabled agent options.
	for _, cfg := range *c {
		if cfg.Agent == nil {
			cfg.Agent = &AgentOptions{Enable: true}
		}
	}
}
