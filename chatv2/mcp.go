//go:build !386 && !arm

package chatv2

import (
	"context"
	"fmt"
	"strings"

	"csust-got/config"

	einomcp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"go.uber.org/zap"
)

// McpManager manages MCP client connections and tool discovery.
type McpManager struct {
	// clients maps server URL to MCP client
	clients map[string]*mcpclient.Client
}

// NewMcpManager creates a new MCP manager.
func NewMcpManager() *McpManager {
	return &McpManager{
		clients: make(map[string]*mcpclient.Client),
	}
}

// GetToolsFromConfig discovers and returns eino tools from MCP server configurations.
// It filters tools based on the allowed tool names in each McpoConfig.
func (m *McpManager) GetToolsFromConfig(ctx context.Context, mcpConfigs []*config.McpoConfig) ([]tool.BaseTool, error) {
	var allTools []tool.BaseTool

	for _, cfg := range mcpConfigs {
		if cfg == nil || !cfg.Enable || cfg.Url == "" {
			continue
		}

		tools, err := m.getToolsFromServer(ctx, cfg)
		if err != nil {
			zap.L().Error("chatv2/mcp: failed to get tools from server",
				zap.String("url", cfg.Url),
				zap.Error(err),
			)
			continue // graceful degradation: skip failed servers
		}

		allTools = append(allTools, tools...)
	}

	return allTools, nil
}

// getToolsFromServer connects to an MCP server and retrieves filtered tools.
func (m *McpManager) getToolsFromServer(ctx context.Context, cfg *config.McpoConfig) ([]tool.BaseTool, error) {
	// Reuse existing client if one exists for this URL
	cli, exists := m.clients[cfg.Url]
	if !exists {
		// Create SSE client for HTTP-based MCP servers
		var err error
		cli, err = mcpclient.NewSSEMCPClient(cfg.Url)
		if err != nil {
			return nil, fmt.Errorf("failed to create MCP client for %s: %w", cfg.Url, err)
		}
		// Store for cleanup
		m.clients[cfg.Url] = cli
	}

	// Build tool name filter
	var toolNames []string
	for _, t := range cfg.Tools {
		toolNames = append(toolNames, strings.TrimSpace(t))
	}

	// Get tools via eino MCP integration
	mcpTools, err := einomcp.GetTools(ctx, &einomcp.Config{
		Cli:          cli,
		ToolNameList: toolNames,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get tools from MCP server %s: %w", cfg.Url, err)
	}

	// Convert to BaseTool slice
	baseTools := make([]tool.BaseTool, 0, len(mcpTools))
	baseTools = append(baseTools, mcpTools...)

	zap.L().Info("chatv2/mcp: discovered tools",
		zap.String("url", cfg.Url),
		zap.Int("count", len(baseTools)),
	)

	return baseTools, nil
}

// Close shuts down all MCP client connections.
func (m *McpManager) Close() {
	for url, cli := range m.clients {
		if err := cli.Close(); err != nil {
			zap.L().Warn("chatv2/mcp: failed to close MCP client",
				zap.String("url", url),
				zap.Error(err),
			)
		}
	}
}
