//go:build !386 && !arm

package chatv2

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"csust-got/config"

	einomcp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// McpManager manages MCP client connections and tool discovery.
type McpManager struct {
	mu sync.Mutex
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
	cli, err := m.getOrCreateClient(ctx, cfg.Url)
	if err != nil {
		return nil, err
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

func (m *McpManager) getOrCreateClient(ctx context.Context, serverURL string) (*mcpclient.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cli, ok := m.clients[serverURL]; ok {
		return cli, nil
	}

	cli, err := mcpclient.NewSSEMCPClient(serverURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP client for %s: %w", serverURL, err)
	}

	if err := initializeMCPClient(ctx, cli); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("failed to initialize MCP client for %s: %w", serverURL, err)
	}

	m.clients[serverURL] = cli
	return cli, nil
}

type mcpLifecycleClient interface {
	Start(ctx context.Context) error
	Initialize(ctx context.Context, request mcp.InitializeRequest) (*mcp.InitializeResult, error)
}

func initializeMCPClient(ctx context.Context, cli mcpLifecycleClient) error {
	if err := cli.Start(ctx); err != nil {
		return fmt.Errorf("start client: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "csust-got chatv2",
		Version: "dev",
	}
	initReq.Params.Capabilities = mcp.ClientCapabilities{}

	if _, err := cli.Initialize(ctx, initReq); err != nil {
		return fmt.Errorf("initialize client: %w", err)
	}

	return nil
}

// Close shuts down all MCP client connections.
func (m *McpManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for url, cli := range m.clients {
		if err := cli.Close(); err != nil {
			zap.L().Warn("chatv2/mcp: failed to close MCP client",
				zap.String("url", url),
				zap.Error(err),
			)
		}
	}
}
