package chat_v2

import (
	"context"
	"csust-got/config"
	"csust-got/log"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/samber/lo"
	"github.com/sashabaranov/go-openai"
	"github.com/swaggest/openapi-go/openapi31"
	"go.uber.org/zap"
)

var mcpo *McpoClient

// McpoClient for mcpo call
type McpoClient struct {
	c       *http.Client
	baseUrl string
	tools   []string

	mcpTools map[string]*McpoTool
	toolSets map[string][]string
}

// McpoTool is config of mcpo tool
type McpoTool struct {
	Url  string
	Name string

	openai.Tool
}

// NewMcpoClient create a new [McpoClient]
func NewMcpoClient(baseUrl string, tools []string) *McpoClient {
	return &McpoClient{
		c:       http.DefaultClient,
		baseUrl: baseUrl,
		tools:   tools,

		mcpTools: map[string]*McpoTool{},
		toolSets: map[string][]string{},
	}
}

// InitMcpoClient init global mcpo client
func InitMcpoClient() {
	cnf := config.BotConfig.McpoServer
	if cnf.Enable {
		mcpo = NewMcpoClient(cnf.Url, cnf.Tools).WithHttpClient(&http.Client{
			Timeout: time.Second * 120,
		})
		err := mcpo.Init()
		if err != nil {
			log.Fatal("failed to init mcpo client", zap.Error(err))
		}
	}
}

// Init init mcpo client
func (c *McpoClient) Init() error {
	allTools := []string{}
	for _, tool := range c.tools {
		tools, err := c.getToolDefined(tool)
		if err != nil {
			return err
		}
		mcpoTools := []string{}
		for _, tool := range tools {
			c.mcpTools[tool.Name] = tool
			allTools = append(allTools, tool.Name)
			mcpoTools = append(mcpoTools, tool.Name)
		}
		if len(mcpoTools) > 0 {
			c.toolSets["mcpo_"+tool] = mcpoTools
		}
	}
	c.toolSets["_default"] = allTools

	return nil
}

// WithHttpClient set http client
func (c *McpoClient) WithHttpClient(client *http.Client) *McpoClient {
	c.c = client
	return c
}

// GetToolSetToolNames get tool names with set name
func (c *McpoClient) GetToolSetToolNames(set string) []string {
	if set == "" {
		set = "_default"
	}
	if toolSet, ok := c.toolSets[set]; ok {
		return toolSet
	}
	return nil
}

// GetToolSet get tool set with set name
func (c *McpoClient) GetToolSet(set string) []openai.Tool {
	toolNames := c.GetToolSetToolNames(set)
	ret := make([]openai.Tool, 0, len(toolNames))
	for _, name := range toolNames {
		tool := c.mcpTools[name]
		ret = append(ret, tool.Tool)
	}
	return ret
}

// GetTool get tool
func (c *McpoClient) GetTool(name string) (*McpoTool, bool) {
	ret, ok := c.mcpTools[name]
	return ret, ok
}

func (c *McpoClient) getUrl(path string) string {
	if c.baseUrl == "" {
		return ""
	}
	baseUrl := strings.TrimSuffix(c.baseUrl, "/")
	return baseUrl + path
}

func (c *McpoClient) getToolDefined(tool string) ([]*McpoTool, error) {
	cl := c.c
	url := c.getUrl("/" + tool)
	resp, err := cl.Get(url + "/openapi.json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	spec := &openapi31.Spec{}
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if err := spec.UnmarshalJSON(buf); err != nil {
		return nil, err
	}

	ret := specToTool(url, spec)
	return ret, nil
}

// ErrInvaliableParameter error
var ErrInvaliableParameter = errors.New("invaliable parameter")

// Call call mcpo tool
func (t *McpoTool) Call(ctx context.Context, param string) (result string, err error) {
	req, err := http.NewRequestWithContext(ctx, "POST", t.Url, strings.NewReader(param))
	if err != nil {
		return result, err
	}
	switch {
	case param == "":
		// do nothing
	case json.Valid([]byte(param)):
		req.Header.Add("Content-Type", "application/json")
	default:
		return result, ErrInvaliableParameter
	}
	resp, err := mcpo.c.Do(req)
	if err != nil {
		return result, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// nolint: err113
		return result, fmt.Errorf("HTTP status code not ok: %d", resp.StatusCode)
	}

	buf := strings.Builder{}
	_, err = io.Copy(&buf, resp.Body)
	if err != nil {
		return result, err
	}
	return buf.String(), nil
}

func specToTool(base string, spec *openapi31.Spec) []*McpoTool {
	var functions = []*McpoTool{}

	paths := spec.Paths.MapOfPathItemValues
	for path, pathItem := range paths {
		op := pathItem.Post
		if op == nil {
			log.Warn("cannot get spec for function", zap.String("path", path))
			continue
		}

		var schema any
		if requestBody := op.RequestBody; requestBody != nil {
			content := requestBody.RequestBody.Content
			if jsonContent, ok := content["application/json"]; ok {
				schema = jsonContent.Schema
			}
		}

		if schema == nil {
			paramsSchema := map[string]any{
				"type": "object",
			}
			if params := op.Parameters; len(params) > 0 {
				props := map[string]any{}
				required := []string{}
				for _, p := range params {
					param := p.Parameter
					prop := param.Schema
					if lo.FromPtr(param.Required) {
						required = append(required, param.Name)
					}
					props[param.Name] = prop
				}
				paramsSchema["properties"] = props
				paramsSchema["required"] = required
			}
			schema = paramsSchema
		}

		fn := &McpoTool{
			Tool: openai.Tool{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        lo.FromPtr(op.ID),
					Description: lo.FromPtr(op.Description),
					Parameters:  schema,
				},
			},
			Url:  base + path,
			Name: lo.FromPtr(op.ID),
		}
		functions = append(functions, fn)

	}
	return functions
}
