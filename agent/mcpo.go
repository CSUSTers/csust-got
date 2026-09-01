//go:build !386 && !arm

package agentv3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"csust-got/config"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	jsonschemaLib "github.com/eino-contrib/jsonschema"
	"github.com/samber/lo"
	"github.com/swaggest/openapi-go/openapi31"
	"go.uber.org/zap"
)

type mcpoTool struct {
	info       *schema.ToolInfo
	url        string
	apiKey     string
	httpClient *http.Client
}

var errMcpoHTTPStatus = errors.New("MCPO HTTP error")

func (t *mcpoTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *mcpoTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", t.url, strings.NewReader(argumentsInJSON))
	if err != nil {
		return "", err
	}
	if argumentsInJSON != "" && json.Valid([]byte(argumentsInJSON)) {
		req.Header.Set("Content-Type", "application/json")
	}
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MCPO HTTP status %d: %w", resp.StatusCode, errMcpoHTTPStatus)
	}

	var buf strings.Builder
	_, err = io.Copy(&buf, resp.Body)
	return buf.String(), err
}

var _ tool.InvokableTool = (*mcpoTool)(nil)

func discoverMcpoToolset(ctx context.Context, baseUrl, apiKey string, entry config.ToolEntry) ([]tool.BaseTool, error) {
	baseUrl = strings.TrimSuffix(baseUrl, "/")
	toolsetUrl := baseUrl + "/" + entry.Name

	spec, err := fetchOpenAPISpec(ctx, toolsetUrl+"/openapi.json")
	if err != nil {
		return nil, fmt.Errorf("fetch OpenAPI spec for %q: %w", entry.Name, err)
	}

	tools := openAPISpecToTools(toolsetUrl, apiKey, spec)

	if len(entry.Tools) > 0 {
		allowed := make(map[string]struct{}, len(entry.Tools))
		for _, t := range entry.Tools {
			allowed[t] = struct{}{}
		}
		filtered := tools[:0]
		for _, t := range tools {
			info, infoErr := t.Info(ctx)
			if infoErr != nil {
				continue
			}
			if _, ok := allowed[info.Name]; ok {
				filtered = append(filtered, t)
			}
		}

		if len(filtered) < len(entry.Tools) {
			discoveredNames := make([]string, 0, len(tools))
			for _, t := range tools {
				if info, e := t.Info(ctx); e == nil {
					discoveredNames = append(discoveredNames, info.Name)
				}
			}
			zap.L().Warn("agentv3/mcpo: some configured tools not found in toolset",
				zap.String("toolset", entry.Name),
				zap.Strings("configured", entry.Tools),
				zap.Strings("available", discoveredNames),
			)
		}
		tools = filtered
	}

	return tools, nil
}

func fetchOpenAPISpec(ctx context.Context, url string) (*openapi31.Spec, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	spec := &openapi31.Spec{}
	if err := spec.UnmarshalJSON(buf); err != nil {
		return nil, err
	}
	return spec, nil
}

func openAPISpecToTools(baseUrl, apiKey string, spec *openapi31.Spec) []tool.BaseTool {
	if spec.Paths == nil {
		return nil
	}

	var tools []tool.BaseTool
	for path, pathItem := range spec.Paths.MapOfPathItemValues {
		op := pathItem.Post
		if op == nil {
			continue
		}

		paramSchema := extractParamSchema(spec, op)
		name := strings.ReplaceAll(strings.Trim(path, "/"), "/", "_")
		desc := lo.CoalesceOrEmpty(
			lo.FromPtr(op.Description),
			lo.FromPtr(lo.FromPtr(op.ExternalDocs).Description),
		)

		var paramsOneOf *schema.ParamsOneOf
		if paramSchema != nil {
			schemaBytes, err := json.Marshal(paramSchema)
			if err == nil {
				jsSchema := &jsonschemaLib.Schema{}
				if json.Unmarshal(schemaBytes, jsSchema) == nil {
					paramsOneOf = schema.NewParamsOneOfByJSONSchema(jsSchema)
				}
			}
		}

		tools = append(tools, &mcpoTool{
			info: &schema.ToolInfo{
				Name:        name,
				Desc:        desc,
				ParamsOneOf: paramsOneOf,
			},
			url:        baseUrl + path,
			apiKey:     apiKey,
			httpClient: http.DefaultClient,
		})
	}
	return tools
}

func extractParamSchema(spec *openapi31.Spec, op *openapi31.Operation) map[string]any {
	if requestBody := op.RequestBody; requestBody != nil {
		content := requestBody.RequestBody.Content
		if jsonContent, ok := content["application/json"]; ok {
			resolved, _ := derefMcpoJSONSchemaRef(spec, jsonContent.Schema)
			return resolved
		}
	}

	paramsSchema := map[string]any{"type": "object"}
	if params := op.Parameters; len(params) > 0 {
		props := map[string]any{}
		var required []string
		for _, p := range params {
			param := p.Parameter
			props[param.Name] = param.Schema
			if lo.FromPtr(param.Required) {
				required = append(required, param.Name)
			}
		}
		paramsSchema["properties"] = props
		paramsSchema["required"] = required
	}
	return paramsSchema
}

const componentsSchemas = "#/components/schemas/"

func derefMcpoJSONSchemaRef(spec *openapi31.Spec, ref map[string]any) (map[string]any, bool) {
	done := maps.Clone(ref)

	if spec == nil || spec.Components == nil || spec.Components.Schemas == nil {
		return done, false
	}

	r, ok := ref["$ref"]
	s, ok2 := r.(string)
	if !ok || !ok2 {
		return done, false
	}

	rr := strings.TrimPrefix(s, componentsSchemas)
	os, found := spec.Components.Schemas[rr]
	if !found {
		return done, false
	}

	delete(done, "$ref")
	if osp, ok := os["properties"].(map[string]any); ok {
		for k, v := range osp {
			if propRef, ok := v.(map[string]any); ok {
				switch propRef["type"] {
				case "string", "number", "integer", "boolean":
					continue
				case "array":
					if items, ok := propRef["items"].(map[string]any); ok {
						derefItems, derefed := derefMcpoJSONSchemaRef(spec, items)
						if derefed {
							propRef["items"] = derefItems
						}
					}
					continue
				default:
					derefProp, derefed := derefMcpoJSONSchemaRef(spec, propRef)
					if derefed {
						osp[k] = derefProp
					}
				}
			}
		}
		os["properties"] = osp
	}
	maps.Copy(done, os)

	return done, found
}
