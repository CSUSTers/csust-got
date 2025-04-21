package chat_v2

import (
	"context"
	"csust-got/config"
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
	"go.uber.org/zap"
)

var mcpClients map[string]*client.Client
var toolsClientMap map[string]string
var allTools []openai.Tool

// InitMcpClients initializes the MCP clients and tools
func InitMcpClients() {
	mcpClients = make(map[string]*client.Client)
	toolsClientMap = make(map[string]string)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range *config.BotConfig.McpServers {
		c, err := client.NewStdioMCPClient(srv.Command, srv.Env, srv.Args...)
		if err != nil {
			panic(err)
		}

		initRequest := mcp.InitializeRequest{}
		initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initRequest.Params.ClientInfo = mcp.Implementation{
			Name:    "csust-got",
			Version: "1.0.0",
		}

		initResult, err := c.Initialize(ctx, initRequest)
		if err != nil {
			panic(err)
		}

		ts, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			zap.L().Error("Failed to list tools", zap.String("mcp-server", srv.Name), zap.Error(err))
			continue
		}

		for _, tool := range ts.Tools {
			schema, err := json.Marshal(tool.InputSchema)
			if err != nil {
				zap.L().Error("Failed to marshal tool schema", zap.String("mcp-server", srv.Name), zap.String("tool", tool.Name), zap.Error(err))
				continue
			}
			toolSchema := jsonschema.Definition{}
			err = json.Unmarshal(schema, &toolSchema)
			if err != nil {
				zap.L().Error("Failed to unmarshal tool schema", zap.String("mcp-server", srv.Name), zap.String("tool", tool.Name), zap.Error(err))
				continue
			}
			ot := openai.Tool{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  toolSchema,
				},
			}
			toolsClientMap[tool.Name] = srv.Name
			allTools = append(allTools, ot)
		}

		mcpClients[srv.Name] = c
		zap.L().Info("MCP Server initialized", zap.String("name", initResult.ServerInfo.Name), zap.String("version", initResult.ServerInfo.Version))
	}

}
