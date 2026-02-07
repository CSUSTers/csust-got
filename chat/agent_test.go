package chat

import (
	"testing"

	"csust-got/config"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/tmc/langchaingo/llms"
)

func TestConvertToLangchainMessages_SystemAndUser(t *testing.T) {
	msgs := convertToLangchainMessages("You are a bot", "Hello!", false, "")
	assert.Len(t, msgs, 2)
	assert.Equal(t, llms.ChatMessageTypeSystem, msgs[0].Role)
	assert.Len(t, msgs[0].Parts, 1)
	tc, ok := msgs[0].Parts[0].(llms.TextContent)
	assert.True(t, ok)
	assert.Equal(t, "You are a bot", tc.Text)

	assert.Equal(t, llms.ChatMessageTypeHuman, msgs[1].Role)
	assert.Len(t, msgs[1].Parts, 1)
	tc2, ok := msgs[1].Parts[0].(llms.TextContent)
	assert.True(t, ok)
	assert.Equal(t, "Hello!", tc2.Text)
}

func TestConvertToLangchainMessages_NoSystemPrompt(t *testing.T) {
	msgs := convertToLangchainMessages("", "Hello!", false, "")
	assert.Len(t, msgs, 1)
	assert.Equal(t, llms.ChatMessageTypeHuman, msgs[0].Role)
}

func TestConvertToLangchainMessages_WithImage(t *testing.T) {
	imageURL := "data:image/jpeg;base64,/9j/test..."
	msgs := convertToLangchainMessages("System", "Describe this", true, imageURL)
	assert.Len(t, msgs, 2)

	// User message should have image + text parts
	userMsg := msgs[1]
	assert.Equal(t, llms.ChatMessageTypeHuman, userMsg.Role)
	assert.Len(t, userMsg.Parts, 2)

	// First part should be image
	imgPart, ok := userMsg.Parts[0].(llms.ImageURLContent)
	assert.True(t, ok)
	assert.Equal(t, imageURL, imgPart.URL)

	// Second part should be text
	textPart, ok := userMsg.Parts[1].(llms.TextContent)
	assert.True(t, ok)
	assert.Equal(t, "Describe this", textPart.Text)
}

func TestConvertToLangchainMessages_WithImageNoURL(t *testing.T) {
	// When multiPartContent is true but imageURL is empty, should fall back to text-only
	msgs := convertToLangchainMessages("System", "Hello", true, "")
	assert.Len(t, msgs, 2)
	userMsg := msgs[1]
	assert.Len(t, userMsg.Parts, 1)
	_, ok := userMsg.Parts[0].(llms.TextContent)
	assert.True(t, ok)
}

func TestAgentConfig_GetMaxIterations(t *testing.T) {
	tests := []struct {
		name     string
		config   config.AgentConfig
		expected int
	}{
		{
			name:     "default value",
			config:   config.AgentConfig{},
			expected: 10,
		},
		{
			name:     "custom value",
			config:   config.AgentConfig{MaxIterations: 5},
			expected: 5,
		},
		{
			name:     "zero uses default",
			config:   config.AgentConfig{MaxIterations: 0},
			expected: 10,
		},
		{
			name:     "negative uses default",
			config:   config.AgentConfig{MaxIterations: -1},
			expected: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetMaxIterations()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetLangchainTools_NilMcpo(t *testing.T) {
	// When mcpo is nil, should return nil
	oldMcpo := mcpo
	mcpo = nil
	defer func() { mcpo = oldMcpo }()

	tools := GetLangchainTools("")
	assert.Nil(t, tools)
}

func TestGetLangchainToolAdapters_NilMcpo(t *testing.T) {
	// When mcpo is nil, should return nil
	oldMcpo := mcpo
	mcpo = nil
	defer func() { mcpo = oldMcpo }()

	adapters := GetLangchainToolAdapters("")
	assert.Nil(t, adapters)
}

func TestGetLangchainModel_NilModels(t *testing.T) {
	// When lcModels is nil, should return nil
	oldModels := lcModels
	lcModels = nil
	defer func() { lcModels = oldModels }()

	model := getLangchainModel("test")
	assert.Nil(t, model)
}

func TestGetLangchainModel_EmptyModels(t *testing.T) {
	oldModels := lcModels
	lcModels = make(map[string]llms.Model)
	defer func() { lcModels = oldModels }()

	model := getLangchainModel("nonexistent")
	assert.Nil(t, model)
}

func TestMcpoToolAdapter_Interface(t *testing.T) {
	// Verify McpoToolAdapter implements tools.Tool interface
	adapter := &McpoToolAdapter{
		mcpoTool: &McpoTool{
			Name: "test-tool",
			Tool: openaiTool("test-tool", "A test tool"),
		},
	}

	assert.Equal(t, "test-tool", adapter.Name())
	assert.Equal(t, "A test tool", adapter.Description())
}

// openaiTool is a helper to create an openai.Tool for testing
func openaiTool(name, desc string) openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        name,
			Description: desc,
		},
	}
}
