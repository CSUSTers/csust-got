package chat_v2

import (
	"context"
	"csust-got/config"
	"csust-got/log"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
var mcpToolsDesc string

// InitMcpClients initializes the MCP clients and tools
func InitMcpClients() {
	mcpClients = make(map[string]*client.Client)
	toolsClientMap = make(map[string]string)

	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "csust-got",
		Version: "1.0.0",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// nolint:gocritic
	for _, srv := range *config.BotConfig.McpServers {

		c, err := client.NewStdioMCPClient(srv.Command, srv.Env, srv.Args...)
		if err != nil {
			log.Fatal("Failed to create mcp client", zap.String("mcp", srv.Name),
				zap.String("command", srv.Command), zap.Strings("env", srv.Env), zap.Strings("args", srv.Args), zap.Error(err))
		}

		initResult, err := c.Initialize(ctx, initRequest)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				log.Error("Init mcp client matches timeout", zap.String("mcp", srv.Name),
					zap.String("command", srv.Command), zap.Strings("env", srv.Env), zap.Strings("args", srv.Args), zap.Error(err))
				continue
			}
			log.Fatal("Failed to init mcp client", zap.String("mcp", srv.Name),
				zap.String("command", srv.Command), zap.Strings("env", srv.Env), zap.Strings("args", srv.Args), zap.Error(err))
		}

		ts, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			log.Error("Failed to list tools", zap.String("mcp-server", srv.Name), zap.Error(err))
			continue
		}

		for _, tool := range ts.Tools {
			schema, err := json.Marshal(tool.InputSchema)
			if err != nil {
				log.Error("Failed to marshal tool schema", zap.String("mcp-server", srv.Name), zap.String("tool", tool.Name), zap.Error(err))
				continue
			}
			toolSchema := jsonschema.Definition{}
			err = json.Unmarshal(schema, &toolSchema)
			if err != nil {
				log.Error("Failed to unmarshal tool schema", zap.String("mcp-server", srv.Name), zap.String("tool", tool.Name), zap.Error(err))
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
		log.Info("MCP Server initialized", zap.String("name", initResult.ServerInfo.Name), zap.String("version", initResult.ServerInfo.Version))
	}

	mcpToolsDesc = getMcpToolsDesc(allTools)
}

func getMcpToolsDesc(tools []openai.Tool) string {
	if len(tools) == 0 {
		return ""
	}

	buf := strings.Builder{}
	buf.WriteString("<tools>\n")
	for _, tool := range tools {
		parameters, err := json.Marshal(tool.Function.Parameters)
		if err != nil {
			log.Error("failed to build tool-use system prompt, cause of cannot get tool parameters schema",
				zap.String("tool_name", tool.Function.Name), zap.Error(err))
			continue
		}
		buf.WriteString("<tool>\n\n")
		buf.WriteString(fmt.Sprintf(`
  <name>%s</name>
  <description>%s</description>
  <arguments>`, tool.Function.Name, tool.Function.Description))
		buf.Write(parameters)
		buf.WriteString("</arguments>\n</tool>\n\n")
	}
	buf.WriteString("</tools>\n")
	return buf.String()
}
