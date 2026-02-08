package chat

import (
	"context"
	"csust-got/log"
	"os"
	"sync"
	"testing"

	"csust-got/config"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
	lctools "github.com/tmc/langchaingo/tools"
)

func TestMain(m *testing.M) {
	// Initialize minimal config and logger for tests that call log functions
	config.BotConfig = &config.Config{DebugMode: true}
	log.InitLogger()
	os.Exit(m.Run())
}

// fakeModel implements llms.Model for testing.
type fakeModel struct {
	// responses is a queue of responses to return from GenerateContent.
	// Each call pops the first response off the queue.
	responses []*llms.ContentResponse
	callCount int
	mu        sync.Mutex
}

func (f *fakeModel) GenerateContent(_ context.Context, _ []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.callCount >= len(f.responses) {
		return &llms.ContentResponse{}, nil
	}
	resp := f.responses[f.callCount]
	f.callCount++
	return resp, nil
}

func (f *fakeModel) Call(_ context.Context, _ string, _ ...llms.CallOption) (string, error) {
	return "", nil
}

// fakeTool implements lctools.Tool for testing.
type fakeTool struct {
	name   string
	desc   string
	result string
}

func (ft *fakeTool) Name() string                                     { return ft.name }
func (ft *fakeTool) Description() string                              { return ft.desc }
func (ft *fakeTool) Call(_ context.Context, _ string) (string, error) { return ft.result, nil }

var _ lctools.Tool = (*fakeTool)(nil)

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

// newTestAgentProcessor creates an agentProcessor suitable for unit tests.
// It disables streaming (no ticker goroutine, no Telegram calls in updateStreamingMessage).
func newTestAgentProcessor(model llms.Model, cfg *config.ChatConfigSingle, msgs []llms.MessageContent) *agentProcessor {
	return &agentProcessor{
		chatCtx:    context.Background(),
		model:      model,
		config:     cfg,
		messages:   msgs,
		done:       make(chan struct{}),
		tickerDone: make(chan struct{}),
	}
}

func TestStreamingCallback_Accumulation(t *testing.T) {
	ap := newTestAgentProcessor(nil, &config.ChatConfigSingle{}, nil)
	close(ap.tickerDone) // no ticker goroutine

	// Simulate streaming chunks
	require.NoError(t, ap.streamingCallback(t.Context(), []byte("Hello ")))
	require.NoError(t, ap.streamingCallback(t.Context(), []byte("world")))
	require.NoError(t, ap.streamingCallback(t.Context(), []byte("!")))

	ap.mu.Lock()
	result := ap.fullResponse.String()
	ap.mu.Unlock()

	assert.Equal(t, "Hello world!", result)
}

func TestExecuteToolCalls_NormalResponse(t *testing.T) {
	cfg := &config.ChatConfigSingle{}
	ap := newTestAgentProcessor(nil, cfg, nil)
	close(ap.tickerDone)

	// Set up a fake tool adapter
	ap.toolAdapters = map[string]lctools.Tool{
		"get_weather": &fakeTool{name: "get_weather", desc: "Get weather", result: `{"temp": 72}`},
	}

	toolCalls := []llms.ToolCall{
		{
			ID:   "call_1",
			Type: "function",
			FunctionCall: &llms.FunctionCall{
				Name:      "get_weather",
				Arguments: `{"city": "NYC"}`,
			},
		},
	}

	err := ap.executeToolCalls(toolCalls)
	require.NoError(t, err)

	// Verify tool response was added to messages
	require.Len(t, ap.messages, 1)
	assert.Equal(t, llms.ChatMessageTypeTool, ap.messages[0].Role)

	// Verify the tool call response content
	require.Len(t, ap.messages[0].Parts, 1)
	resp, ok := ap.messages[0].Parts[0].(llms.ToolCallResponse)
	require.True(t, ok)
	assert.Equal(t, "call_1", resp.ToolCallID)
	assert.Equal(t, "get_weather", resp.Name)
	assert.Equal(t, `{"temp": 72}`, resp.Content)
}

func TestExecuteToolCalls_ToolNotFound(t *testing.T) {
	cfg := &config.ChatConfigSingle{}
	ap := newTestAgentProcessor(nil, cfg, nil)
	close(ap.tickerDone)

	ap.toolAdapters = map[string]lctools.Tool{}

	toolCalls := []llms.ToolCall{
		{
			ID:   "call_1",
			Type: "function",
			FunctionCall: &llms.FunctionCall{
				Name:      "nonexistent",
				Arguments: "{}",
			},
		},
	}

	err := ap.executeToolCalls(toolCalls)
	require.NoError(t, err) // does not return error, adds error message to messages

	require.Len(t, ap.messages, 1)
	resp, ok := ap.messages[0].Parts[0].(llms.ToolCallResponse)
	require.True(t, ok)
	assert.Contains(t, resp.Content, "Tool not found")
}

func TestBuildCallOptions_WithTools(t *testing.T) {
	cfg := &config.ChatConfigSingle{}
	cfg.Format.StreamOutput = false
	ap := newTestAgentProcessor(nil, cfg, nil)
	close(ap.tickerDone)

	// Add some tool definitions
	ap.toolDefs = []llms.Tool{
		{Type: "function", Function: &llms.FunctionDefinition{Name: "test"}},
	}

	opts := ap.buildCallOptions()
	// Should have temperature + tools (no streaming since StreamOutput is false)
	assert.Len(t, opts, 2)
}

func TestBuildCallOptions_WithStreaming(t *testing.T) {
	cfg := &config.ChatConfigSingle{}
	cfg.Format.StreamOutput = true
	ap := newTestAgentProcessor(nil, cfg, nil)
	close(ap.tickerDone)

	opts := ap.buildCallOptions()
	// Should have temperature + streaming (no tools)
	assert.Len(t, opts, 2)
}

func TestAgentLoop_NormalResponse(t *testing.T) {
	// Test that the agent loop correctly accumulates a normal (no tool call) response
	model := &fakeModel{
		responses: []*llms.ContentResponse{
			{Choices: []*llms.ContentChoice{{Content: "Hello from agent!"}}},
		},
	}

	cfg := &config.ChatConfigSingle{
		Agent: config.AgentConfig{Enabled: true, MaxIterations: 5},
	}
	msgs := []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("Hi")}},
	}

	ap := newTestAgentProcessor(model, cfg, msgs)
	close(ap.tickerDone) // no streaming ticker

	// Run the agent loop up to model call + accumulation (not finalizeResponse)
	maxIterations := cfg.Agent.GetMaxIterations()
	for iteration := range maxIterations {
		_ = iteration
		opts := ap.buildCallOptions()
		resp, err := ap.model.GenerateContent(ap.chatCtx, ap.messages, opts...)
		require.NoError(t, err)
		require.NotEmpty(t, resp.Choices)

		choice := resp.Choices[0]
		if choice.Content != "" {
			ap.mu.Lock()
			ap.fullResponse.WriteString(choice.Content)
			ap.mu.Unlock()
		}

		if len(choice.ToolCalls) == 0 {
			ap.messages = append(ap.messages, llms.MessageContent{
				Role:  llms.ChatMessageTypeAI,
				Parts: []llms.ContentPart{llms.TextPart(choice.Content)},
			})
			break
		}
	}

	ap.mu.Lock()
	result := ap.fullResponse.String()
	ap.mu.Unlock()

	assert.Equal(t, "Hello from agent!", result)
	// Should have user message + AI response
	assert.Len(t, ap.messages, 2)
	assert.Equal(t, llms.ChatMessageTypeAI, ap.messages[1].Role)
	assert.Equal(t, 1, model.callCount)
}

func TestAgentLoop_ToolCallRoundTrip(t *testing.T) {
	// Test that the agent loop handles a tool call followed by a final response
	model := &fakeModel{
		responses: []*llms.ContentResponse{
			// First response: tool call
			{Choices: []*llms.ContentChoice{{
				Content: "",
				ToolCalls: []llms.ToolCall{{
					ID:   "call_1",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      "get_weather",
						Arguments: `{"city": "NYC"}`,
					},
				}},
			}}},
			// Second response: final answer after tool result
			{Choices: []*llms.ContentChoice{{Content: "The weather in NYC is 72°F."}}},
		},
	}

	cfg := &config.ChatConfigSingle{
		Agent: config.AgentConfig{Enabled: true, MaxIterations: 5},
	}
	msgs := []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("What's the weather?")}},
	}

	ap := newTestAgentProcessor(model, cfg, msgs)
	close(ap.tickerDone)
	ap.toolAdapters = map[string]lctools.Tool{
		"get_weather": &fakeTool{name: "get_weather", desc: "Get weather", result: `{"temp": 72}`},
	}

	// Simulate the agent loop (same logic as run() without finalizeResponse)
	maxIterations := cfg.Agent.GetMaxIterations()
	for iteration := range maxIterations {
		_ = iteration
		opts := ap.buildCallOptions()
		resp, err := ap.model.GenerateContent(ap.chatCtx, ap.messages, opts...)
		require.NoError(t, err)

		if len(resp.Choices) == 0 {
			break
		}
		choice := resp.Choices[0]

		if choice.Content != "" {
			ap.mu.Lock()
			ap.fullResponse.WriteString(choice.Content)
			ap.mu.Unlock()
		}

		if len(choice.ToolCalls) > 0 {
			assistantMsg := llms.MessageContent{
				Role:  llms.ChatMessageTypeAI,
				Parts: []llms.ContentPart{},
			}
			for _, tc := range choice.ToolCalls {
				assistantMsg.Parts = append(assistantMsg.Parts, tc)
			}
			ap.messages = append(ap.messages, assistantMsg)

			err := ap.executeToolCalls(choice.ToolCalls)
			require.NoError(t, err)

			ap.mu.Lock()
			ap.fullResponse.Reset()
			ap.reasonContent.Reset()
			ap.lastSentText = ""
			ap.mu.Unlock()
			continue
		}

		ap.messages = append(ap.messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: []llms.ContentPart{llms.TextPart(choice.Content)},
		})
		break
	}

	ap.mu.Lock()
	result := ap.fullResponse.String()
	ap.mu.Unlock()

	assert.Equal(t, "The weather in NYC is 72°F.", result)
	assert.Equal(t, 2, model.callCount)

	// Messages should be: user, AI+tool_call, tool_response, AI_final
	assert.Len(t, ap.messages, 4)
	assert.Equal(t, llms.ChatMessageTypeHuman, ap.messages[0].Role)
	assert.Equal(t, llms.ChatMessageTypeAI, ap.messages[1].Role)
	assert.Equal(t, llms.ChatMessageTypeTool, ap.messages[2].Role)
	assert.Equal(t, llms.ChatMessageTypeAI, ap.messages[3].Role)
}

func TestAgentLoop_EmptyResponse(t *testing.T) {
	// Model returns empty choices — loop should break gracefully
	model := &fakeModel{
		responses: []*llms.ContentResponse{
			{Choices: []*llms.ContentChoice{}},
		},
	}

	cfg := &config.ChatConfigSingle{
		Agent: config.AgentConfig{Enabled: true, MaxIterations: 5},
	}
	msgs := []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("Hi")}},
	}

	ap := newTestAgentProcessor(model, cfg, msgs)
	close(ap.tickerDone)

	maxIterations := cfg.Agent.GetMaxIterations()
	for range maxIterations {
		resp, err := ap.model.GenerateContent(ap.chatCtx, ap.messages, ap.buildCallOptions()...)
		require.NoError(t, err)
		if len(resp.Choices) == 0 {
			break
		}
	}

	ap.mu.Lock()
	result := ap.fullResponse.String()
	ap.mu.Unlock()

	assert.Empty(t, result)
	assert.Equal(t, 1, model.callCount)
}

func TestAgentLoop_ReasoningContent(t *testing.T) {
	// Test that reasoning content is accumulated
	model := &fakeModel{
		responses: []*llms.ContentResponse{
			{Choices: []*llms.ContentChoice{{
				Content:          "Final answer.",
				ReasoningContent: "Let me think about this...",
			}}},
		},
	}

	cfg := &config.ChatConfigSingle{
		Agent: config.AgentConfig{Enabled: true, MaxIterations: 5},
	}

	ap := newTestAgentProcessor(model, cfg, []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("Think about it")}},
	})
	close(ap.tickerDone)

	resp, err := ap.model.GenerateContent(ap.chatCtx, ap.messages, ap.buildCallOptions()...)
	require.NoError(t, err)
	choice := resp.Choices[0]

	if choice.Content != "" {
		ap.mu.Lock()
		ap.fullResponse.WriteString(choice.Content)
		ap.mu.Unlock()
	}
	if choice.ReasoningContent != "" {
		ap.mu.Lock()
		ap.reasonContent.WriteString(choice.ReasoningContent)
		ap.mu.Unlock()
	}

	ap.mu.Lock()
	content := ap.fullResponse.String()
	reason := ap.reasonContent.String()
	ap.mu.Unlock()

	assert.Equal(t, "Final answer.", content)
	assert.Equal(t, "Let me think about this...", reason)
}
