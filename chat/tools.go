package chat

import (
	"context"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/tools"
)

// McpoToolAdapter wraps an McpoTool to implement the langchaingo tools.Tool interface.
type McpoToolAdapter struct {
	mcpoTool *McpoTool
}

var _ tools.Tool = (*McpoToolAdapter)(nil)

// Name returns the tool name.
func (t *McpoToolAdapter) Name() string {
	return t.mcpoTool.Name
}

// Description returns the tool description.
func (t *McpoToolAdapter) Description() string {
	if t.mcpoTool.Function != nil {
		return t.mcpoTool.Function.Description
	}
	return ""
}

// Call executes the tool with the given input and returns the result.
func (t *McpoToolAdapter) Call(ctx context.Context, input string) (string, error) {
	return t.mcpoTool.Call(ctx, input)
}

// GetLangchainTools converts mcpo tools to langchaingo tool definitions for use with GenerateContent.
// This returns []llms.Tool (function definitions) suitable for passing as call options.
func GetLangchainTools(setName string) []llms.Tool {
	if mcpo == nil {
		return nil
	}
	toolNames := mcpo.GetToolSetToolNames(setName)
	result := make([]llms.Tool, 0, len(toolNames))
	for _, name := range toolNames {
		tool, ok := mcpo.GetTool(name)
		if !ok {
			continue
		}
		result = append(result, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        tool.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
			},
		})
	}
	return result
}

// GetLangchainToolAdapters returns langchaingo tools.Tool adapters for mcpo tools.
// These are the callable implementations used to execute tool calls.
func GetLangchainToolAdapters(setName string) map[string]tools.Tool {
	if mcpo == nil {
		return nil
	}
	toolNames := mcpo.GetToolSetToolNames(setName)
	result := make(map[string]tools.Tool, len(toolNames))
	for _, name := range toolNames {
		tool, ok := mcpo.GetTool(name)
		if !ok {
			continue
		}
		result[name] = &McpoToolAdapter{mcpoTool: tool}
	}
	return result
}
