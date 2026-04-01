//go:build !386 && !arm

package chatv2

import (
	"context"
	"csust-got/config"
	"errors"
	"fmt"
	"strings"
	"text/template"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

var (
	errModelConfigNil    = errors.New("model config is nil")
	errSubAgentConfigNil = errors.New("subagent config is nil")
	errAgentConfigNil    = errors.New("agent config is nil")
)

const finalTurnGuidance = "你已经接近本次任务的步骤上限。这一轮禁止继续调用任何工具，请直接基于已有信息输出最终答案；如果信息仍不足，也只能明确说明卡在哪里、缺什么，不要再继续调工具。"

// buildModel creates an eino ChatModel from a config.Model definition.
func buildModel(ctx context.Context, modelCfg *config.Model) (*einoopenai.ChatModel, error) {
	if modelCfg == nil {
		return nil, errModelConfigNil
	}

	cfg := &einoopenai.ChatModelConfig{
		APIKey:  modelCfg.ApiKey,
		BaseURL: modelCfg.BaseUrl,
		Model:   modelCfg.Model,
	}

	model, err := einoopenai.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create chat model %q: %w", modelCfg.Name, err)
	}

	return model, nil
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
func buildMainAgent(ctx context.Context, chatCfg *config.ChatConfigSingle, mcpMgr *McpManager) (*react.Agent, error) {
	agentCfg := chatCfg.Agent
	if agentCfg == nil {
		return nil, fmt.Errorf("%w for chat %q", errAgentConfigNil, chatCfg.Name)
	}

	// Build the main model
	mainModel, err := buildModel(ctx, chatCfg.Model)
	if err != nil {
		return nil, fmt.Errorf("failed to build model for chat %q: %w", chatCfg.Name, err)
	}

	// Merge skill configurations into effective tools/mcpServers/toolModels
	effectiveTools, effectiveMcpServers, effectiveToolModels := mergeSkillConfigs(agentCfg)

	// Collect all tools
	var allTools []tool.BaseTool

	// 1. Built-in tools (agent's own + from skills)
	if len(effectiveTools) > 0 {
		builtins, err := BuildBuiltinTools(effectiveTools, effectiveToolModels)
		if err != nil {
			return nil, fmt.Errorf("failed to build tools for chat %q: %w", chatCfg.Name, err)
		}
		allTools = append(allTools, builtins...)
	}

	// 2. MCP tools (agent's own + from skills)
	if len(effectiveMcpServers) > 0 && mcpMgr != nil {
		mcpTools, err := mcpMgr.GetToolsFromConfig(ctx, effectiveMcpServers)
		if err != nil {
			zap.L().Warn("chatv2/agent: failed to get MCP tools, continuing without them",
				zap.String("chat", chatCfg.Name),
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
			continue // graceful degradation
		}
		allTools = append(allTools, subTool)
	}

	// 4. Wrap all tools with error handler so errors become model-readable messages
	allTools = wrapToolsWithErrorHandler(allTools)

	maxSteps := agentCfg.GetMaxSteps()
	if agentCfg.MaxSteps > 0 && agentHasTools(agentCfg) && maxSteps != agentCfg.MaxSteps {
		zap.L().Warn("chatv2/agent: main agent max_steps too low for tool-enabled workflow, clamped",
			zap.String("chat", chatCfg.Name),
			zap.Int("configured", agentCfg.MaxSteps),
			zap.Int("effective", maxSteps),
		)
	}

	// Create the react agent
	// System prompt is included via BuildMessages(), not MessageModifier
	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: mainModel,
		MaxStep:          maxSteps,
		MessageModifier:  newFinalTurnMessageModifier(maxSteps),
		ToolsConfig: compose.ToolsNodeConfig{
			Tools:               allTools,
			UnknownToolsHandler: newUnknownToolsHandler(allTools),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create main agent for chat %q: %w", chatCfg.Name, err)
	}

	zap.L().Info("chatv2/agent: built main agent",
		zap.String("chat", chatCfg.Name),
		zap.Int("total_tools", len(allTools)),
		zap.Int("subagents", len(agentCfg.SubAgents)),
		zap.Int("skills", len(agentCfg.Skills)),
		zap.Int("max_steps", maxSteps),
	)

	return agent, nil
}

func newFinalTurnMessageModifier(maxSteps int) react.MessageModifier {
	return func(_ context.Context, input []*schema.Message) []*schema.Message {
		if !shouldInjectFinalTurnGuidance(input, maxSteps) {
			return input
		}

		out := make([]*schema.Message, 0, len(input)+1)
		out = append(out, schema.SystemMessage(finalTurnGuidance))
		out = append(out, input...)
		return out
	}
}

func shouldInjectFinalTurnGuidance(messages []*schema.Message, maxSteps int) bool {
	if maxSteps <= 0 {
		return false
	}

	toolRounds := 0
	for _, msg := range messages {
		if msg == nil || len(msg.ToolCalls) == 0 {
			continue
		}
		toolRounds++
	}

	// Current model step is 2*toolRounds+1. If another tool call is made now,
	// the agent would need two more graph steps (tools + final model answer).
	return toolRounds*2+3 > maxSteps
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
	for k, v := range agentCfg.ToolModels {
		toolModels[k] = v
	}

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
func CompileChat(ctx context.Context, chatCfg *config.ChatConfigSingle, mcpMgr *McpManager) (*CompiledChat, error) {
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

	// Build the main agent
	agent, err := buildMainAgent(ctx, chatCfg, mcpMgr)
	if err != nil {
		return nil, fmt.Errorf("failed to build main agent for %q: %w", chatCfg.Name, err)
	}

	return &CompiledChat{
		Name:              chatCfg.Name,
		Config:            chatCfg,
		Agent:             agent,
		SystemTemplate:    systemTpl,
		PromptTemplate:    promptTpl,
		SkillPromptAddons: GetSkillPromptAddons(chatCfg.Agent),
	}, nil
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
