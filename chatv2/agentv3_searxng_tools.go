//go:build !386 && !arm

package chatv2

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	jsonschemaLib "github.com/eino-contrib/jsonschema"
)

const (
	agentV3ToolSearXNGWebSearch      = "searxng_web_search"
	agentV3ToolSearXNGSuggestions    = "searxng_search_suggestions"
	agentV3ToolSearXNGInstanceInfo   = "searxng_instance_info"
	agentV3SearXNGSkillName          = "searxng"
	agentV3SearXNGActivationRequired = "[SearXNG Error] load_skill(\"searxng\") is required for this turn."
)

type searXNGWebSearchTool struct{ client *searXNGClient }
type searXNGSuggestionsTool struct{ client *searXNGClient }
type searXNGInstanceInfoTool struct{ client *searXNGClient }

func (t *searXNGWebSearchTool) Info(context.Context) (*schema.ToolInfo, error) {
	maxResults := 20
	if t.client != nil && t.client.config.MaxResults > 0 && t.client.config.MaxResults <= 20 {
		maxResults = t.client.config.MaxResults
	}
	return newSearXNGToolInfo(agentV3ToolSearXNGWebSearch, "Search the configured SearXNG instance after loading the searxng skill.", fmt.Sprintf(`{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string","description":"Non-empty search query."},"pageno":{"type":"integer","minimum":1,"description":"Positive result page."},"time_range":{"type":"string","enum":["day","week","month","year"],"description":"Optional recency filter."},"language":{"type":"string","description":"Optional search language."},"safesearch":{"type":"integer","enum":[0,1,2],"description":"Optional safety level."},"min_score":{"type":"number","description":"Optional finite minimum result score."},"num_results":{"type":"integer","minimum":1,"maximum":%d,"description":"Number of results."},"categories":{"type":"string","description":"Optional comma-separated category tokens."},"engines":{"type":"string","description":"Optional comma-separated engine tokens."},"response_format":{"type":"string","enum":["text","json"],"description":"Output format."},"result_detail":{"type":"string","enum":["full","compact"],"description":"Result detail level."}},"required":["query"]}`, maxResults))
}

func (t *searXNGSuggestionsTool) Info(context.Context) (*schema.ToolInfo, error) {
	return newSearXNGToolInfo(agentV3ToolSearXNGSuggestions, "Get search suggestions from the configured SearXNG instance after loading the searxng skill.", `{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string","description":"Non-empty suggestion query."},"language":{"type":"string","description":"Optional suggestion language."}},"required":["query"]}`)
}

func (t *searXNGInstanceInfoTool) Info(context.Context) (*schema.ToolInfo, error) {
	return newSearXNGToolInfo(agentV3ToolSearXNGInstanceInfo, "Get bounded public metadata from the configured SearXNG instance after loading the searxng skill.", `{"type":"object","additionalProperties":false,"properties":{"include_engines":{"type":"boolean","description":"Include matching engine summaries."},"include_disabled":{"type":"boolean","description":"Include disabled engines when engines are included."},"category":{"type":"string","description":"Optional engine category token."}}}`)
}

func newSearXNGToolInfo(name, desc, rawSchema string) (*schema.ToolInfo, error) {
	params := &jsonschemaLib.Schema{}
	if err := json.Unmarshal([]byte(rawSchema), params); err != nil {
		return nil, err
	}
	return &schema.ToolInfo{Name: name, Desc: desc, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(params)}, nil
}

func (t *searXNGWebSearchTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	if tc := GetTurnContext(ctx); tc == nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGWebSearch, errNoTurnContext)
	} else if !tc.hasLoadedSkill(agentV3SearXNGSkillName) {
		return agentV3SearXNGActivationRequired, nil
	}
	var args searXNGWebSearchArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGWebSearch, newSearXNGError(searXNGErrorRequestFailed))
	}
	if t.client == nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGWebSearch, newSearXNGError(searXNGErrorUnavailable))
	}
	output, err := t.client.WebSearch(ctx, args)
	if err != nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGWebSearch, err)
	}
	return output, nil
}

func (t *searXNGSuggestionsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	if tc := GetTurnContext(ctx); tc == nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGSuggestions, errNoTurnContext)
	} else if !tc.hasLoadedSkill(agentV3SearXNGSkillName) {
		return agentV3SearXNGActivationRequired, nil
	}
	var args searXNGSuggestionsArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGSuggestions, newSearXNGError(searXNGErrorRequestFailed))
	}
	if t.client == nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGSuggestions, newSearXNGError(searXNGErrorUnavailable))
	}
	output, err := t.client.SearchSuggestions(ctx, args)
	if err != nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGSuggestions, err)
	}
	return output, nil
}

func (t *searXNGInstanceInfoTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	if tc := GetTurnContext(ctx); tc == nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGInstanceInfo, errNoTurnContext)
	} else if !tc.hasLoadedSkill(agentV3SearXNGSkillName) {
		return agentV3SearXNGActivationRequired, nil
	}
	var args searXNGInstanceInfoArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGInstanceInfo, newSearXNGError(searXNGErrorRequestFailed))
	}
	if t.client == nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGInstanceInfo, newSearXNGError(searXNGErrorUnavailable))
	}
	output, err := t.client.InstanceInfo(ctx, args)
	if err != nil {
		return "", fmt.Errorf("%s: %w", agentV3ToolSearXNGInstanceInfo, err)
	}
	return output, nil
}

var _ tool.InvokableTool = (*searXNGWebSearchTool)(nil)
var _ tool.InvokableTool = (*searXNGSuggestionsTool)(nil)
var _ tool.InvokableTool = (*searXNGInstanceInfoTool)(nil)
