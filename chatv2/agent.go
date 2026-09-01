//go:build !386 && !arm

package chatv2

import (
	"context"
	"csust-got/config"
	"errors"
	"fmt"
	"maps"
	"strings"
	"text/template"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

var (
	errModelConfigNil    = errors.New("model config is nil")
	errSubAgentConfigNil = errors.New("subagent config is nil")
	errAgentConfigNil    = errors.New("agent config is nil")
	errMaxStepsInvalid   = errors.New("max_steps must be > 0")
	errDownstreamClosed  = errors.New("downstream consumer closed")
)

type guidanceLevel int

const (
	guidanceNone guidanceLevel = iota
	guidanceSoft
	guidanceHard
)

const softTurnGuidance = "你已经进行了 %d 轮工具调用。如果你认为已经收集到足够的信息来回答用户的问题，请直接整理已有信息并输出最终回答，不需要继续调用工具。"

const finalTurnGuidance = "你已经接近本次任务的步骤上限。这一轮禁止继续调用任何工具，请直接基于已有信息输出最终答案；如果信息仍不足，也只能明确说明卡在哪里、缺什么，不要再继续调工具。"

const agentV3MinToolMaxSteps = 4

// buildModel creates an eino ChatModel from a config.Model definition.
func buildModel(ctx context.Context, modelCfg *config.Model) (model.ToolCallingChatModel, error) {
	if modelCfg == nil {
		return nil, errModelConfigNil
	}

	cfg := &einoopenai.ChatModelConfig{
		APIKey:  modelCfg.ApiKey,
		BaseURL: modelCfg.BaseUrl,
		Model:   modelCfg.Model,
	}

	chatModel, err := einoopenai.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create chat model %q: %w", modelCfg.Name, err)
	}

	return newRetryingChatModel(chatModel, modelCfg), nil
}

// buildSubAgentTool creates a subagent wrapped as a tool.BaseTool.
// The subagent uses the ADK ChatModelAgent and is callable by the main agent.
func buildSubAgentTool(ctx context.Context, subCfg *config.SubAgentConfig, mcpMgr *McpManager) (tool.BaseTool, error) {
	if subCfg == nil {
		return nil, errSubAgentConfigNil
	}

	// Build the subagent's model
	subModel, err := buildModel(ctx, subCfg.Model)
	if err != nil {
		return nil, fmt.Errorf("failed to build model for subagent %q: %w", subCfg.Name, err)
	}

	// Collect subagent tools: built-in + MCP
	var subTools []tool.BaseTool

	if len(subCfg.Tools) > 0 {
		builtins, err := BuildBuiltinTools(subCfg.Tools, subCfg.ToolModels)
		if err != nil {
			return nil, fmt.Errorf("failed to build tools for subagent %q: %w", subCfg.Name, err)
		}
		subTools = append(subTools, builtins...)
	}

	if len(subCfg.McpServers) > 0 && mcpMgr != nil {
		mcpTools, err := mcpMgr.GetToolsFromConfig(ctx, subCfg.McpServers)
		if err != nil {
			zap.L().Warn("chatv2/agent: failed to get MCP tools for subagent, continuing without them",
				zap.String("subagent", subCfg.Name),
				zap.Error(err),
			)
		} else {
			subTools = append(subTools, mcpTools...)
		}
	}

	// Build the ADK agent
	systemPrompt := subCfg.SystemPrompt.String()
	if systemPrompt == "" {
		systemPrompt = fmt.Sprintf("You are %s. %s", subCfg.Name, subCfg.Description)
	}
	maxSteps := subCfg.GetMaxSteps()
	if subCfg.MaxSteps > 0 && subAgentHasTools(subCfg) && maxSteps != subCfg.MaxSteps {
		zap.L().Warn("chatv2/agent: subagent max_steps too low for tool-enabled workflow, clamped",
			zap.String("subagent", subCfg.Name),
			zap.Int("configured", subCfg.MaxSteps),
			zap.Int("effective", maxSteps),
		)
	}

	adkAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          subCfg.Name,
		Description:   subCfg.Description,
		Instruction:   systemPrompt,
		Model:         subModel,
		MaxIterations: maxSteps,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               subTools,
				UnknownToolsHandler: newUnknownToolsHandler(subTools),
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create ADK agent for subagent %q: %w", subCfg.Name, err)
	}

	// Wrap as tool
	agentTool := adk.NewAgentTool(ctx, adkAgent)

	zap.L().Info("chatv2/agent: built subagent tool",
		zap.String("name", subCfg.Name),
		zap.Int("tools", len(subTools)),
		zap.Int("max_steps", maxSteps),
	)

	return agentTool, nil
}

// buildMainAgent creates the main react.Agent from a ChatConfigSingle with agent config.
// It assembles all tools: built-in + MCP + subagent tools + skill tools.
func buildMainAgent(ctx context.Context, chatCfg *config.ChatConfigSingle, mcpMgr *McpManager, skillCatalog agentV3SkillCatalog, startup *agentV3StartupSkillSnapshots) (*CustomAgent, error) {
	agentCfg := chatCfg.Agent
	if agentCfg == nil {
		return nil, fmt.Errorf("%w for chat %q", errAgentConfigNil, chatCfg.Name)
	}

	modelCfg := chatCfg.Model
	if chatCfg.IsAgentV3Enabled() && config.BotConfig != nil && config.BotConfig.AgentV3 != nil {
		modelCfg = config.BotConfig.AgentV3.EffectiveModel(chatCfg.Model)
	}

	// Build the main model
	mainModel, err := buildModel(ctx, modelCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build model for chat %q: %w", chatCfg.Name, err)
	}

	allTools, err := buildConfiguredAgentTools(ctx, chatCfg.Name, agentCfg, mcpMgr)
	if err != nil {
		return nil, err
	}
	if chatCfg.IsAgentV3Enabled() {
		var cfg *config.AgentV3Config
		if config.BotConfig != nil {
			cfg = config.BotConfig.AgentV3
		}
		var searxng *searXNGClient
		if startup != nil {
			searxng = startup.SearXNG
		}
		warnAgentV3SearXNGToolCollisions(ctx, chatCfg.Name, searxng, allTools)
		allTools = append(buildAgentV3Tools(chatCfg, cfg, skillCatalog, searxng), allTools...)
	}
	allTools = wrapToolsWithErrorHandler(allTools)

	maxSteps := agentCfg.GetMaxSteps()
	if chatCfg.IsAgentV3Enabled() && maxSteps < agentV3MinToolMaxSteps {
		maxSteps = agentV3MinToolMaxSteps
	}
	if agentCfg.MaxSteps > 0 && (agentHasTools(agentCfg) || chatCfg.IsAgentV3Enabled()) && maxSteps != agentCfg.MaxSteps {
		zap.L().Warn("chatv2/agent: main agent max_steps too low for tool-enabled workflow, clamped",
			zap.String("chat", chatCfg.Name),
			zap.Int("configured", agentCfg.MaxSteps),
			zap.Int("effective", maxSteps),
		)
	}

	agent, err := NewCustomAgent(ctx, &CustomAgentConfig{
		Name:     chatCfg.Name,
		Model:    mainModel,
		Tools:    allTools,
		MaxSteps: maxSteps,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create main agent for chat %q: %w", chatCfg.Name, err)
	}

	logMsg := "chatv2/agent: built main agent"
	if chatCfg.IsAgentV3Enabled() {
		logMsg = "chatv2/agent: built agent v3"
	}
	zap.L().Info(logMsg,
		zap.String("chat", chatCfg.Name),
		zap.Int("total_tools", len(allTools)),
		zap.Int("subagents", len(agentCfg.SubAgents)),
		zap.Int("skills", len(agentCfg.Skills)),
		zap.Int("max_steps", maxSteps),
	)

	return agent, nil
}

func buildConfiguredAgentTools(ctx context.Context, chatName string, agentCfg *config.AgentConfig, mcpMgr *McpManager) ([]tool.BaseTool, error) {
	// Merge skill configurations into effective tools/mcpServers/toolModels
	effectiveTools, effectiveMcpServers, effectiveToolModels := mergeSkillConfigs(agentCfg)

	var allTools []tool.BaseTool

	// 1. Built-in tools (agent's own + from skills)
	if len(effectiveTools) > 0 {
		builtins, err := BuildBuiltinTools(effectiveTools, effectiveToolModels)
		if err != nil {
			return nil, fmt.Errorf("failed to build tools for chat %q: %w", chatName, err)
		}
		allTools = append(allTools, builtins...)
	}

	// 2. MCP tools (agent's own + from skills)
	if len(effectiveMcpServers) > 0 && mcpMgr != nil {
		mcpTools, err := mcpMgr.GetToolsFromConfig(ctx, effectiveMcpServers)
		if err != nil {
			zap.L().Warn("chatv2/agent: failed to get MCP tools, continuing without them",
				zap.String("chat", chatName),
				zap.Error(err),
			)
		} else {
			allTools = append(allTools, mcpTools...)
		}
	}

	// 3. Subagent tools
	for _, subCfg := range agentCfg.SubAgents {
		subTool, err := buildSubAgentTool(ctx, subCfg, mcpMgr)
		if err != nil {
			zap.L().Error("chatv2/agent: failed to build subagent, skipping",
				zap.String("subagent", subCfg.Name),
				zap.Error(err),
			)
			continue
		}
		allTools = append(allTools, subTool)
	}

	return allTools, nil
}

// calcGuidanceLevel determines what kind of guidance (if any) to inject based
// on how many tool rounds have been used relative to the step budget.
//
// Each tool round consumes 2 steps (model call + tool execution). The current
// model call is step toolRounds*2+1, so remaining = maxSteps - (toolRounds*2+1).
//
// Returns:
//   - guidanceNone: plenty of budget left, let the model work freely
//   - guidanceSoft: budget getting tight, nudge the model to wrap up if it has enough info
//   - guidanceHard: near the limit, forbid further tool calls (fallback)
func calcGuidanceLevel(messages []*schema.Message, maxSteps int) (guidanceLevel, int) {
	if maxSteps <= 0 {
		return guidanceNone, 0
	}

	toolRounds := 0
	for _, msg := range messages {
		if msg == nil || len(msg.ToolCalls) == 0 {
			continue
		}
		toolRounds++
	}

	if toolRounds == 0 {
		return guidanceNone, 0
	}

	remaining := maxSteps - (toolRounds*2 + 1)

	switch {
	case remaining <= 3:
		// Near the limit — forbid further tool calls.
		return guidanceHard, toolRounds
	case toolRounds >= 2 && remaining*3 < maxSteps*2:
		// Less than 2/3 of budget remains and model has done ≥2 rounds —
		// softly suggest wrapping up if it has enough info.
		return guidanceSoft, toolRounds
	default:
		return guidanceNone, toolRounds
	}
}

func subAgentHasTools(cfg *config.SubAgentConfig) bool {
	return cfg != nil && (len(cfg.Tools) > 0 || len(cfg.McpServers) > 0)
}

func agentHasTools(cfg *config.AgentConfig) bool {
	if cfg == nil {
		return false
	}
	if len(cfg.Tools) > 0 || len(cfg.McpServers) > 0 || len(cfg.SubAgents) > 0 {
		return true
	}
	for _, skill := range cfg.Skills {
		if skill != nil && (len(skill.Tools) > 0 || len(skill.McpServers) > 0) {
			return true
		}
	}
	return false
}

func mergeSkillConfigs(agentCfg *config.AgentConfig) (
	tools []string,
	mcpServers []*config.ToolServerConfig,
	toolModels map[string]*config.Model,
) {
	tools = append(tools, agentCfg.Tools...)
	mcpServers = append(mcpServers, agentCfg.McpServers...)

	toolModels = make(map[string]*config.Model)
	maps.Copy(toolModels, agentCfg.ToolModels)

	toolSeen := make(map[string]struct{})
	for _, t := range agentCfg.Tools {
		toolSeen[t] = struct{}{}
	}

	for _, skill := range agentCfg.Skills {
		if skill == nil {
			continue
		}
		for _, t := range skill.Tools {
			if _, exists := toolSeen[t]; !exists {
				tools = append(tools, t)
				toolSeen[t] = struct{}{}
			}
		}
		mcpServers = append(mcpServers, skill.McpServers...)
		for k, v := range skill.ToolModels {
			if _, exists := toolModels[k]; !exists {
				toolModels[k] = v
			}
		}
	}
	return tools,
		mcpServers,
		toolModels
}

func wrapToolsWithErrorHandler(tools []tool.BaseTool) []tool.BaseTool {
	wrapped := make([]tool.BaseTool, len(tools))
	for i, t := range tools {
		wrapped[i] = toolutils.WrapToolWithErrorHandler(t, toolErrorHandler)
	}
	return wrapped
}

func toolErrorHandler(_ context.Context, err error) string {
	return fmt.Sprintf("[Tool Error] %s\nPlease try a different approach or adjust parameters.", err.Error())
}

// GetSkillPromptAddons returns the concatenated system prompt addons for all skills in the agent config.
func GetSkillPromptAddons(agentCfg *config.AgentConfig) string {
	if agentCfg == nil {
		return ""
	}
	var addons []string
	for _, skill := range agentCfg.Skills {
		if skill == nil {
			continue
		}
		if addon := skill.SystemPromptAddon.String(); addon != "" {
			addons = append(addons, addon)
		}
	}
	return strings.Join(addons, "\n\n")
}

// CompileChat pre-compiles templates and builds the main agent for a chat configuration.
// Called at Init() time; the returned CompiledChat is reused for every request.
func CompileChat(ctx context.Context, chatCfg *config.ChatConfigSingle, mcpMgr *McpManager, startup *agentV3StartupSkillSnapshots) (*CompiledChat, error) {
	// Compile system prompt template
	var systemTpl *template.Template
	if s := chatCfg.SystemPrompt.String(); s != "" {
		var err error
		systemTpl, err = template.New("system").Parse(s)
		if err != nil {
			return nil, fmt.Errorf("failed to parse system prompt template for %q: %w", chatCfg.Name, err)
		}
	}

	// Compile user prompt template
	var promptTpl *template.Template
	if p := chatCfg.PromptTemplate.String(); p != "" {
		var err error
		promptTpl, err = template.New("prompt").Parse(p)
		if err != nil {
			return nil, fmt.Errorf("failed to parse prompt template for %q: %w", chatCfg.Name, err)
		}
	}

	var sources []agentV3SkillSnapshot
	var catalog agentV3SkillCatalog
	if chatCfg.IsAgentV3Enabled() && config.BotConfig != nil && config.BotConfig.AgentV3 != nil {
		var shadows []agentV3SkillShadow
		var err error
		sources, catalog, shadows, err = compileAgentV3SkillCatalog(chatCfg, config.BotConfig.AgentV3, startup)
		if err != nil {
			return nil, fmt.Errorf("compile agent v3 skills for %q: %w", chatCfg.Name, err)
		}
		for _, shadow := range shadows {
			zap.L().Warn("chatv2/agent: agent v3 skill shadowed",
				zap.String("chat", chatCfg.Name),
				zap.String("name", shadow.Name),
				zap.String("winner_source", string(shadow.Winner.Source)),
				zap.String("loser_source", string(shadow.Loser.Source)),
				zap.String("winner_sha256", shadow.Winner.SHA256),
				zap.String("loser_sha256", shadow.Loser.SHA256),
			)
		}
	}

	// Build the main agent
	agent, err := buildMainAgent(ctx, chatCfg, mcpMgr, catalog, startup)
	if err != nil {
		return nil, fmt.Errorf("failed to build main agent for %q: %w", chatCfg.Name, err)
	}

	skillAddons := GetSkillPromptAddons(chatCfg.Agent)

	return &CompiledChat{
		Name:                 chatCfg.Name,
		Config:               chatCfg,
		Agent:                agent,
		SystemTemplate:       systemTpl,
		PromptTemplate:       promptTpl,
		SkillPromptAddons:    skillAddons,
		AgentV3StartupSkills: startup,
		AgentV3SkillSources:  sources,
		AgentV3SkillCatalog:  catalog,
	}, nil
}

func warnAgentV3SearXNGToolCollisions(ctx context.Context, chat string, searxng *searXNGClient, configured []tool.BaseTool) {
	if searxng == nil {
		return
	}
	native := map[string]struct{}{
		agentV3ToolSearXNGWebSearch:    {},
		agentV3ToolSearXNGSuggestions:  {},
		agentV3ToolSearXNGInstanceInfo: {},
	}
	warned := make(map[string]struct{})
	for _, candidate := range configured {
		info, err := candidate.Info(ctx)
		if err != nil {
			continue
		}
		if _, ok := native[info.Name]; !ok {
			continue
		}
		if _, ok := warned[info.Name]; ok {
			continue
		}
		warned[info.Name] = struct{}{}
		zap.L().Warn("chatv2/agent: native SearXNG tool selected by native-first registration",
			zap.String("chat", chat),
			zap.String("tool", info.Name),
		)
	}
}

func compileAgentV3SkillCatalog(chatCfg *config.ChatConfigSingle, cfg *config.AgentV3Config, startup *agentV3StartupSkillSnapshots) ([]agentV3SkillSnapshot, agentV3SkillCatalog, []agentV3SkillShadow, error) {
	botLocal := emptyAgentV3SkillSnapshot(agentV3SkillSourceBotLocal)
	runtimeGlobal := emptyAgentV3SkillSnapshot(agentV3SkillSourceRuntimeGlobal)
	if startup != nil {
		botLocal = cloneAgentV3SkillSnapshotForChat(startup.BotLocal)
		runtimeGlobal = cloneAgentV3SkillSnapshotForChat(startup.RuntimeGlobal)
	}
	sources := []agentV3SkillSnapshot{
		buildAgentV3BuiltinSkillSnapshot(chatCfg, cfg),
		botLocal,
		runtimeGlobal,
	}
	catalog, shadows, err := mergeAgentV3SkillSnapshots(sources...)
	if err != nil {
		return nil, agentV3SkillCatalog{}, nil, err
	}
	return sources, catalog, shadows, nil
}

func cloneAgentV3SkillSnapshotForChat(snapshot agentV3SkillSnapshot) agentV3SkillSnapshot {
	clone := agentV3SkillSnapshot{
		SchemaVersion:  snapshot.SchemaVersion,
		SnapshotSHA256: strings.Clone(snapshot.SnapshotSHA256),
		Skills:         make([]agentV3SkillDescriptor, len(snapshot.Skills)),
	}
	for i, descriptor := range snapshot.Skills {
		clone.Skills[i] = cloneAgentV3SkillDescriptor(descriptor)
	}
	return clone
}

// newUnknownToolsHandler returns a handler that reports available tool names when the model
// calls a tool that doesn't exist. This prevents eino from returning a hard error on tool name
// hallucination — instead the model gets an informative message and can self-correct.
func newUnknownToolsHandler(tools []tool.BaseTool) func(ctx context.Context, name, input string) (string, error) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if info, err := t.Info(context.Background()); err == nil {
			names = append(names, info.Name)
		}
	}
	return func(ctx context.Context, name, input string) (string, error) {
		zap.L().Warn("chatv2/agent: model called unknown tool",
			zap.String("tool", name),
			zap.Strings("available", names),
		)
		return fmt.Sprintf(
			"[Tool Error] Tool %q does not exist. Available tools: %s. Please use one of the available tool names exactly.",
			name, strings.Join(names, ", "),
		), nil
	}
}
