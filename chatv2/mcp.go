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

type McpManager struct {
	mu      sync.Mutex
	clients map[string]*mcpclient.Client
}

func NewMcpManager() *McpManager {
	return &McpManager{
		clients: make(map[string]*mcpclient.Client),
	}
}

func (m *McpManager) GetToolsFromConfig(ctx context.Context, cfgs []*config.ToolServerConfig) ([]tool.BaseTool, error) {
	var allTools []tool.BaseTool

	for _, cfg := range cfgs {
		if cfg == nil || !cfg.Enable || cfg.Url == "" {
			continue
		}

		var tools []tool.BaseTool
		var err error

		switch cfg.GetType() {
		case config.ToolServerTypeMCPO:
			tools, err = m.getToolsFromMcpo(ctx, cfg)
		default:
			tools, err = m.getToolsFromMCP(ctx, cfg)
		}

		if err != nil {
			zap.L().Error("chatv2/mcp: failed to get tools from server",
				zap.String("url", cfg.Url),
				zap.String("type", cfg.GetType()),
				zap.Error(err),
			)
			continue
		}

		allTools = append(allTools, tools...)
	}

	return allTools, nil
}

func (m *McpManager) getToolsFromMCP(ctx context.Context, cfg *config.ToolServerConfig) ([]tool.BaseTool, error) {
	cli, err := m.getOrCreateClient(ctx, cfg)
	if err != nil {
		return nil, err
	}

	toolNames := cfg.Tools.Names()
	var trimmedNames []string
	for _, t := range toolNames {
		trimmedNames = append(trimmedNames, strings.TrimSpace(t))
	}

	mcpTools, err := einomcp.GetTools(ctx, &einomcp.Config{
		Cli:          cli,
		ToolNameList: trimmedNames,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get tools from MCP server %s: %w", cfg.Url, err)
	}

	discoveredNames := make([]string, 0, len(mcpTools))
	for _, t := range mcpTools {
		info, infoErr := t.Info(ctx)
		if infoErr == nil && info != nil {
			discoveredNames = append(discoveredNames, info.Name)
		}
	}

	if len(trimmedNames) > 0 {
		discoveredSet := make(map[string]struct{}, len(discoveredNames))
		for _, n := range discoveredNames {
			discoveredSet[n] = struct{}{}
		}
		var missing []string
		for _, n := range trimmedNames {
			if _, ok := discoveredSet[n]; !ok {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			zap.L().Warn("chatv2/mcp: configured tool names not found on server",
				zap.String("url", cfg.Url),
				zap.Strings("missing", missing),
				zap.Strings("available", discoveredNames),
			)
		}
	}

	baseTools := make([]tool.BaseTool, 0, len(mcpTools))
	baseTools = append(baseTools, mcpTools...)

	zap.L().Info("chatv2/mcp: discovered MCP tools",
		zap.String("url", cfg.Url),
		zap.String("transport", cfg.GetType()),
		zap.Int("count", len(baseTools)),
		zap.Strings("names", discoveredNames),
	)

	return baseTools, nil
}

func (m *McpManager) getToolsFromMcpo(ctx context.Context, cfg *config.ToolServerConfig) ([]tool.BaseTool, error) {
	var allTools []tool.BaseTool

	for _, entry := range cfg.Tools {
		tools, err := discoverMcpoToolset(ctx, cfg.Url, cfg.ApiKey, entry)
		if err != nil {
			zap.L().Error("chatv2/mcp: failed to discover MCPO toolset",
				zap.String("url", cfg.Url),
				zap.String("toolset", entry.Name),
				zap.Error(err),
			)
			continue
		}
		allTools = append(allTools, tools...)
	}

	if len(allTools) > 0 {
		names := make([]string, 0, len(allTools))
		for _, t := range allTools {
			if info, err := t.Info(ctx); err == nil && info != nil {
				names = append(names, info.Name)
			}
		}
		zap.L().Info("chatv2/mcp: discovered MCPO tools",
			zap.String("url", cfg.Url),
			zap.Int("count", len(allTools)),
			zap.Strings("names", names),
		)
	}

	return allTools, nil
}

func (m *McpManager) getOrCreateClient(ctx context.Context, cfg *config.ToolServerConfig) (*mcpclient.Client, error) {
	cacheKey := cfg.GetType() + "|" + cfg.Url

	m.mu.Lock()
	defer m.mu.Unlock()

	if cli, ok := m.clients[cacheKey]; ok {
		return cli, nil
	}

	var cli *mcpclient.Client
	var err error

	switch cfg.GetType() {
	case config.ToolServerTypeStreamableHTTP:
		cli, err = mcpclient.NewStreamableHttpClient(cfg.Url)
	default:
		cli, err = mcpclient.NewSSEMCPClient(cfg.Url)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP client (%s) for %s: %w", cfg.GetType(), cfg.Url, err)
	}

	if err := initializeMCPClient(ctx, cli); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("failed to initialize MCP client for %s: %w", cfg.Url, err)
	}

	m.clients[cacheKey] = cli
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
