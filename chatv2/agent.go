package chatv2

import (
	"context"
	"errors"
	"fmt"
	"text/template"

	"csust-got/config"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"go.uber.org/zap"
)

// buildModel creates an eino ChatModel from a config.Model definition.
func buildModel(ctx context.Context, modelCfg *config.Model) (*einoopenai.ChatModel, error) {
	if modelCfg == nil {
		return nil, errors.New("model config is nil")
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
		return nil, errors.New("subagent config is nil")
	}

	// Build the subagent's model
	subModel, err := buildModel(ctx, subCfg.Model)
	if err != nil {
		return nil, fmt.Errorf("failed to build model for subagent %q: %w", subCfg.Name, err)
	}

	// Collect subagent tools: built-in + MCP
	var subTools []tool.BaseTool

	if len(subCfg.Tools) > 0 {
		builtins, err := BuildBuiltinTools(subCfg.Tools)
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

	adkAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          subCfg.Name,
		Description:   subCfg.Description,
		Instruction:   systemPrompt,
		Model:         subModel,
		MaxIterations: subCfg.GetMaxSteps(),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: subTools,
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
	)

	return agentTool, nil
}

// buildMainAgent creates the main react.Agent from a ChatConfigSingle with agent config.
// It assembles all tools: built-in + MCP + subagent tools.
func buildMainAgent(ctx context.Context, chatCfg *config.ChatConfigSingle, mcpMgr *McpManager) (*react.Agent, error) {
	agentCfg := chatCfg.Agent
	if agentCfg == nil {
		return nil, fmt.Errorf("agent config is nil for chat %q", chatCfg.Name)
	}

	// Build the main model
	mainModel, err := buildModel(ctx, chatCfg.Model)
	if err != nil {
		return nil, fmt.Errorf("failed to build model for chat %q: %w", chatCfg.Name, err)
	}

	// Collect all tools
	var allTools []tool.BaseTool

	// 1. Built-in tools
	if len(agentCfg.Tools) > 0 {
		builtins, err := BuildBuiltinTools(agentCfg.Tools)
		if err != nil {
			return nil, fmt.Errorf("failed to build tools for chat %q: %w", chatCfg.Name, err)
		}
		allTools = append(allTools, builtins...)
	}

	// 2. MCP tools
	if len(agentCfg.McpServers) > 0 && mcpMgr != nil {
		mcpTools, err := mcpMgr.GetToolsFromConfig(ctx, agentCfg.McpServers)
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

	// Create the react agent
	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: mainModel,
		MaxStep:          agentCfg.GetMaxSteps(),
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: allTools,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create main agent for chat %q: %w", chatCfg.Name, err)
	}

	zap.L().Info("chatv2/agent: built main agent",
		zap.String("chat", chatCfg.Name),
		zap.Int("total_tools", len(allTools)),
		zap.Int("subagents", len(agentCfg.SubAgents)),
	)

	return agent, nil
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
		Name:           chatCfg.Name,
		Config:         chatCfg,
		Agent:          agent,
		SystemTemplate: systemTpl,
		PromptTemplate: promptTpl,
	}, nil
}
