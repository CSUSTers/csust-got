package chat

import (
	"context"
	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// streamProcessor encapsulates the state and configuration for processing streaming responses
type streamProcessor struct {
	chatCtx        context.Context
	ctx            tb.Context
	placeholderMsg *tb.Message
	useMcp         bool
	request        *openai.ChatCompletionRequest
	messages       *[]openai.ChatCompletionMessage
	config         *config.ChatConfigSingle

	// State variables
	fullResponse           strings.Builder
	reasoningContent       strings.Builder // Native reasoning content from OpenAI protocol
	lastSentText           string
	ticker                 *time.Ticker
	done                   chan struct{}
	currentToolCallsChunks []openai.ToolCall // Store all tool call chunks in a flat slice

	// Mutex to protect concurrent access to strings.Builder
	mu sync.RWMutex
}

// newStreamProcessor creates a new streamProcessor with the provided configuration
func newStreamProcessor(chatCtx context.Context, ctx tb.Context, placeholderMsg *tb.Message, useMcp bool, request *openai.ChatCompletionRequest, messages *[]openai.ChatCompletionMessage, chatConfig *config.ChatConfigSingle) *streamProcessor {
	return &streamProcessor{
		chatCtx:        chatCtx,
		ctx:            ctx,
		placeholderMsg: placeholderMsg,
		useMcp:         useMcp,
		request:        request,
		messages:       messages,
		config:         chatConfig,
		done:           make(chan struct{}), // dont use a buffered channel to ensure proper synchronization
	}
}

// findLastSentenceDelimiter finds the last occurrence of any sentence delimiter in the text
// Returns the end position of the delimiter (position after the delimiter)
func findLastSentenceDelimiter(text string, delimiters []string) int {
	lastPos := -1
	lastDelimiterLen := 0
	for _, delimiter := range delimiters {
		if pos := strings.LastIndex(text, delimiter); pos > lastPos {
			lastPos = pos
			lastDelimiterLen = len(delimiter)
		}
	}
	if lastPos == -1 {
		return -1
	}
	return lastPos + lastDelimiterLen // Return the position after the delimiter
}

// startStreamingTicker starts the ticker for real-time message updates
func (sp *streamProcessor) startStreamingTicker() {
	if !sp.config.Format.StreamOutput {
		return
	}

	editInterval := sp.config.Format.GetEditInterval()
	sp.ticker = time.NewTicker(editInterval)

	go func() {
		defer sp.ticker.Stop()

		for {
			select {
			case <-sp.ticker.C:
				sp.updateStreamingMessage()
			case <-sp.done:
				return
			}
		}
	}()
}

// updateStreamingMessage updates the message with accumulated content at sentence boundaries
func (sp *streamProcessor) updateStreamingMessage() {
	sp.mu.RLock()
	currentText := sp.fullResponse.String()
	nativeReason := sp.reasoningContent.String()
	sp.mu.RUnlock()

	if currentText == "" || currentText == sp.lastSentText {
		return
	}

	delimiters := config.BotConfig.SentenceDelimiters
	lastDelimEndPos := findLastSentenceDelimiter(currentText, delimiters)

	if lastDelimEndPos <= 0 {
		return
	}

	textToSend := currentText[:lastDelimEndPos]
	if textToSend == sp.lastSentText {
		return
	}

	formattedText := formatOutputWithReason(textToSend, nativeReason, &sp.config.Format)
	formatOpt := sp.getFormatOption()

	if formattedText == "" {
		return // Skip if formatted text is empty
	}

	var err error
	if sp.placeholderMsg == nil {
		// If no placeholder message exists, create a reply message for the first time
		sp.placeholderMsg, err = util.SendReplyWithError(sp.ctx.Chat(), util.RawTgText(formattedText), sp.ctx.Message(), formatOpt)
		if err != nil {
			log.Error("Failed to create initial reply message during streaming", zap.Error(err))
			return
		}
	} else {
		// Edit the existing placeholder message
		_, err = util.EditMessageWithError(sp.placeholderMsg, util.RawTgText(formattedText), formatOpt)
		if err != nil {
			log.Error("Failed to edit message during streaming", zap.Error(err))
			return
		}
	}

	sp.lastSentText = textToSend
}

// getFormatOption returns the appropriate Telegram formatting option
func (sp *streamProcessor) getFormatOption() tb.ParseMode {
	if sp.config.Format.GetFormat() == config.OutputFormatHTML {
		return tb.ModeHTML
	}
	return tb.ModeMarkdownV2
}

// stopTicker stops the streaming ticker
func (sp *streamProcessor) stopTicker() {
	if sp.ticker != nil {
		sp.done <- struct{}{}
		sp.ticker.Stop()
	}
}

// handleToolCallsInStream processes MCP tool calls and restarts the stream
func (sp *streamProcessor) handleToolCallsInStream(toolCalls []openai.ToolCall) (*openai.ChatCompletionStream, error) {
	// Stop the ticker temporarily for tool processing
	sp.stopTicker()

	// Handle the tool calls using the provided aggregated data
	if err := sp.handleToolCalls(toolCalls); err != nil {
		return nil, err
	}

	// Create a new stream for the follow-up request
	sp.request.Messages = *sp.messages
	newStream, err := clients[sp.config.Model.Name].CreateChatCompletionStream(sp.chatCtx, *sp.request)
	if err != nil {
		return nil, err
	}

	// Reset buffer, reasoning content, and tool calls for the new stream
	sp.mu.Lock()
	sp.fullResponse.Reset()
	sp.reasoningContent.Reset()
	sp.currentToolCallsChunks = nil
	sp.mu.Unlock()
	sp.lastSentText = ""

	// Restart ticker if streaming is enabled
	sp.startStreamingTicker()

	return newStream, nil
}

// processStreamChunk processes a single chunk from the stream
func (sp *streamProcessor) processStreamChunk(choice openai.ChatCompletionStreamChoice) {
	if choice.Delta.Content != "" {
		sp.mu.Lock()
		sp.fullResponse.WriteString(choice.Delta.Content)
		sp.mu.Unlock()
	}

	// Handle native reasoning content from OpenAI protocol
	if choice.Delta.ReasoningContent != "" {
		sp.mu.Lock()
		sp.reasoningContent.WriteString(choice.Delta.ReasoningContent)
		sp.mu.Unlock()
	}

	// Handle tool calls in delta
	if len(choice.Delta.ToolCalls) > 0 {
		sp.mu.Lock()
		sp.currentToolCallsChunks = append(sp.currentToolCallsChunks, choice.Delta.ToolCalls...)
		sp.mu.Unlock()
	}
}

// aggregateToolCalls combines all stored tool call chunks into complete tool calls
func (sp *streamProcessor) aggregateToolCalls() []openai.ToolCall {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	// If no tool calls, return empty slice
	if len(sp.currentToolCallsChunks) == 0 {
		return nil
	}

	toolCallMap := make(map[int]*openai.ToolCall)         // Map to store aggregated tool calls by index
	argumentsBuilderMap := make(map[int]*strings.Builder) // Map to store arguments builders by index, because arguments may vary in length

	// Process all stored tool call chunks
	for _, deltaToolCall := range sp.currentToolCallsChunks {
		// Get the index, default to 0 if nil
		index := 0
		if deltaToolCall.Index != nil {
			index = *deltaToolCall.Index
		}

		// Create entry if it doesn't exist
		if _, exists := toolCallMap[index]; !exists {
			toolCallMap[index] = &openai.ToolCall{
				Index: &index,
				Type:  deltaToolCall.Type,
			}
			argumentsBuilderMap[index] = &strings.Builder{}
		}

		// Accumulate the tool call data
		currentTool := toolCallMap[index]
		if deltaToolCall.ID != "" {
			currentTool.ID += deltaToolCall.ID
		}
		if deltaToolCall.Function.Name != "" {
			currentTool.Function.Name += deltaToolCall.Function.Name
		}
		if deltaToolCall.Function.Arguments != "" {
			argumentsBuilderMap[index].WriteString(deltaToolCall.Function.Arguments)
		}
	}

	// Convert map to slice and sort by index
	result := make([]openai.ToolCall, 0, len(toolCallMap))

	// Build the final tool calls from the map
	for index, toolCall := range toolCallMap {
		// Set the arguments from the builder
		toolCall.Function.Arguments = argumentsBuilderMap[index].String()
		result = append(result, *toolCall)
	}

	// Sort the result by index
	sort.Slice(result, func(i, j int) bool {
		return *result[i].Index < *result[j].Index
	})

	return result
}

// finalizeResponse sends the final response message
func (sp *streamProcessor) finalizeResponse() (*tb.Message, error) {
	// Stop the ticker
	sp.stopTicker()

	// Get the final response and native reasoning content
	sp.mu.RLock()
	finalResponse := strings.TrimSpace(sp.fullResponse.String())
	nativeReason := strings.TrimSpace(sp.reasoningContent.String())
	sp.mu.RUnlock()

	formattedResponse := formatOutputWithReason(finalResponse, nativeReason, &sp.config.Format)
	if formattedResponse == "" {
		log.Warn("Final response is empty, sending error message instead")
		formattedResponse = sp.config.GetErrorMessage()
	}

	// Prepare format option
	formatOpt := sp.getFormatOption()

	var replyMsg *tb.Message
	var err error

	if sp.placeholderMsg != nil {
		// If we have a placeholder, edit it with the final response
		replyMsg, err = util.EditMessageWithError(sp.placeholderMsg, util.RawTgText(formattedResponse), formatOpt)
		if err != nil {
			log.Error("Failed to edit placeholder message with final response", zap.Error(err))
			return nil, err
		}
	} else {
		// If no placeholder, send a new reply
		replyMsg, err = util.SendReplyWithError(sp.ctx.Chat(), util.RawTgText(formattedResponse), sp.ctx.Message(), formatOpt)
		if err != nil {
			log.Error("Failed to send reply", zap.Error(err))
			return nil, err
		}
	}

	// Store the message to Redis
	if storeErr := orm.PushMessageToStream(replyMsg); storeErr != nil {
		log.Warn("Store bot's reply message to Redis stream failed", zap.Error(storeErr))
	}
	if storeErr := orm.SetMessage(replyMsg); storeErr != nil {
		log.Warn("Store bot's reply message to Redis failed", zap.Error(storeErr))
	}

	// Store AI response metadata for potential regeneration
	if sp.ctx.Message() != nil {
		originalPrompt := sp.ctx.Message().Text
		if originalPrompt == "" {
			originalPrompt = sp.ctx.Message().Caption
		}

		allowRegenerate := sp.config.Features.AllowRegenerate
		metadata := &orm.AIResponseMetadata{
			BotMessageID:    replyMsg.ID,
			UserMessageID:   sp.ctx.Message().ID,
			ChatID:          replyMsg.Chat.ID,
			ConfigName:      sp.config.Name,
			OriginalPrompt:  originalPrompt,
			Messages:        *sp.messages,
			RegenerateCount: 0,
			AllowRegenerate: &allowRegenerate,
		}
		if storeErr := orm.SetAIResponseMetadata(metadata); storeErr != nil {
			log.Warn("Failed to store AI response metadata", zap.Error(storeErr))
		}
	}

	return replyMsg, nil
}

// handleToolCalls processes MCP tool calls and returns the updated messages
func (sp *streamProcessor) handleToolCalls(toolCalls []openai.ToolCall) error {
	// Build the complete message from the stream so far
	sp.mu.RLock()
	content := sp.fullResponse.String()
	sp.mu.RUnlock()

	completeMsg := openai.ChatCompletionMessage{
		Role:      openai.ChatMessageRoleAssistant,
		Content:   content,
		ToolCalls: toolCalls,
	}

	// Process tool calls similar to the original implementation
	*sp.messages = append(*sp.messages, completeMsg)
	for _, toolCall := range toolCalls {
		var result string
		var toolErr error
		tool, ok := mcpo.GetTool(toolCall.Function.Name)
		if !ok {
			log.Error("MCP tool not found", zap.String("toolName", toolCall.Function.Name))
			result = "MCP tool not found"
		} else {
			result, toolErr = tool.Call(sp.chatCtx, toolCall.Function.Arguments)
			if toolErr != nil {
				log.Error("Failed to call tool", zap.String("toolName", toolCall.Function.Name), zap.Error(toolErr))
				result = "Failed to call function tool"
			}
		}
		log.Debug("Tool call result", zap.String("toolName", toolCall.Function.Name), zap.String("result", result))

		toolMsg := openai.ChatCompletionMessage{
			Role:       openai.ChatMessageRoleTool,
			ToolCallID: toolCall.ID,
			Name:       toolCall.Function.Name,
			Content:    result,
		}
		*sp.messages = append(*sp.messages, toolMsg)
	}
	return nil
}

// process handles the complete streaming response process
func (sp *streamProcessor) process(stream *openai.ChatCompletionStream) (string, error) {
	// Start streaming ticker if enabled
	sp.startStreamingTicker()
	defer sp.stopTicker()

	currentStream := stream
	defer func() {
		if currentStream != nil {
			if closeErr := currentStream.Close(); closeErr != nil {
				log.Error("Failed to close stream", zap.Error(closeErr))
			}
		}
	}()

	// Process the stream
	finishReason := openai.FinishReasonNull
loop:
	for {
		response, err := currentStream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}

		log.Debug("llm stream", zap.Any("resp", response))

		if len(response.Choices) == 0 {
			continue
		}

		choice := response.Choices[0]

		// Accumulate the content and tool calls
		sp.processStreamChunk(choice)

		finished := false
		finishReason = choice.FinishReason

		// Handle tool calls when the stream indicates completion
		switch finishReason {
		case openai.FinishReasonStop:
			finished = true
			fallthrough
		case openai.FinishReasonFunctionCall, openai.FinishReasonToolCalls:
			switch {
			case sp.useMcp:
				// Check if we have accumulated tool calls
				toolCalls := sp.aggregateToolCalls()

				if len(toolCalls) > 0 {
					// Send bot is typing
					_ = sp.ctx.Bot().Notify(sp.ctx.Chat(), tb.Typing)
					// Handle tool calls in the stream
					newStream, err := sp.handleToolCallsInStream(toolCalls)
					if err != nil {
						return "", err
					}
					// Close current stream and switch to new stream
					if err := currentStream.Close(); err != nil {
						log.Error("Failed to close current stream", zap.Error(err))
					}
					currentStream = newStream
					continue loop
				}
				fallthrough
			case finished:
				break loop
			default:
				// nolint: err113
				return "", errors.New("model requested function call but MCP is disabled")
			}
		case openai.FinishReasonLength, openai.FinishReasonContentFilter:
			log.Warn("Stream finished unexpected",
				zap.String("reason", string(finishReason)), zap.Any("response", sp.fullResponse.String()))
			break loop
		case openai.FinishReasonNull, "":
			// Continue streaming
		default:
			log.Warn("Unknown finish reason", zap.String("reason", string(finishReason)))
		}
	}

	log.Debug("Stream finished", zap.String("reason", string(finishReason)))

	// Finalize and send the response
	_, err := sp.finalizeResponse()
	if err != nil {
		return "", err
	}

	sp.mu.RLock()
	result := strings.TrimSpace(sp.fullResponse.String())
	sp.mu.RUnlock()
	return result, nil
}
