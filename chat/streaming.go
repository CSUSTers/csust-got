package chat

import (
	"context"
	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"errors"
	"io"
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
	format         *config.ChatOutputFormatConfig
	useMcp         bool
	client         *openai.Client
	request        *openai.ChatCompletionRequest
	messages       *[]openai.ChatCompletionMessage
	config         *config.ChatConfigSingle

	// State variables
	fullResponse strings.Builder
	lastSentText string
	ticker       *time.Ticker
	done         chan bool

	// Mutex to protect concurrent access to strings.Builder
	mu sync.RWMutex
}

// newStreamProcessor creates a new streamProcessor with the provided configuration
func newStreamProcessor(chatCtx context.Context, ctx tb.Context, placeholderMsg *tb.Message, format *config.ChatOutputFormatConfig, useMcp bool, client *openai.Client, request *openai.ChatCompletionRequest, messages *[]openai.ChatCompletionMessage, chatConfig *config.ChatConfigSingle) *streamProcessor {
	return &streamProcessor{
		chatCtx:        chatCtx,
		ctx:            ctx,
		placeholderMsg: placeholderMsg,
		format:         format,
		useMcp:         useMcp,
		client:         client,
		request:        request,
		messages:       messages,
		config:         chatConfig,
		done:           make(chan bool, 1),
	}
}

// findLastSentenceDelimiter finds the last occurrence of any sentence delimiter in the text
func findLastSentenceDelimiter(text string, delimiters []string) int {
	lastPos := -1
	for _, delimiter := range delimiters {
		if pos := strings.LastIndex(text, delimiter); pos > lastPos {
			lastPos = pos // Return the position of the delimiter
		}
	}
	return lastPos
}

// startStreamingTicker starts the ticker for real-time message updates
func (sp *streamProcessor) startStreamingTicker() {
	if !sp.format.StreamOutput {
		return
	}

	editInterval := sp.format.GetEditInterval()
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
	sp.mu.RUnlock()

	if currentText == "" || currentText == sp.lastSentText {
		return
	}

	delimiters := config.BotConfig.SentenceDelimiters
	lastDelimPos := findLastSentenceDelimiter(currentText, delimiters)

	if lastDelimPos <= 0 {
		return
	}

	textToSend := currentText[:lastDelimPos+1]
	if textToSend == sp.lastSentText {
		return
	}

	formattedText := formatOutput(textToSend, sp.format)
	formatOpt := sp.getFormatOption()

	if formattedText == "" {
		return // Skip if formatted text is empty
	}

	var err error
	if sp.placeholderMsg == nil {
		// If no placeholder message exists, create a reply message for the first time
		sp.placeholderMsg, err = sp.ctx.Bot().Reply(sp.ctx.Message(), formattedText, formatOpt)
		if err != nil {
			log.Error("Failed to create initial reply message during streaming", zap.Error(err))
			return
		}
	} else {
		// Edit the existing placeholder message
		_, err = util.EditMessageWithError(sp.placeholderMsg, formattedText, formatOpt)
		if err != nil {
			log.Error("Failed to edit message during streaming", zap.Error(err))
			return
		}
	}

	sp.lastSentText = textToSend
}

// getFormatOption returns the appropriate Telegram formatting option
func (sp *streamProcessor) getFormatOption() tb.ParseMode {
	if sp.format.Format == "html" {
		return tb.ModeHTML
	}
	return tb.ModeMarkdownV2
}

// stopTicker stops the streaming ticker
func (sp *streamProcessor) stopTicker() {
	if sp.ticker != nil {
		sp.done <- true
		sp.ticker.Stop()
	}
}

// handleToolCallsInStream processes MCP tool calls and restarts the stream
func (sp *streamProcessor) handleToolCallsInStream(choice openai.ChatCompletionStreamChoice) (*openai.ChatCompletionStream, error) {
	// Stop the ticker temporarily for tool processing
	sp.stopTicker()

	// Handle the tool calls
	if err := sp.handleToolCalls(choice); err != nil {
		return nil, err
	}

	// Create a new stream for the follow-up request
	sp.request.Messages = *sp.messages
	newStream, err := sp.client.CreateChatCompletionStream(sp.chatCtx, *sp.request)
	if err != nil {
		return nil, err
	}

	// Reset buffer for the new stream
	sp.mu.Lock()
	sp.fullResponse.Reset()
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
}

// finalizeResponse sends the final response message
func (sp *streamProcessor) finalizeResponse() (*tb.Message, error) {
	// Stop the ticker
	sp.stopTicker()

	// Get the final response
	sp.mu.RLock()
	finalResponse := strings.TrimSpace(sp.fullResponse.String())
	sp.mu.RUnlock()

	formattedResponse := formatOutput(finalResponse, sp.format)
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
		replyMsg, err = util.EditMessageWithError(sp.placeholderMsg, formattedResponse, formatOpt)
		if err != nil {
			log.Error("Failed to edit placeholder message with final response", zap.Error(err))
			return nil, err
		}
	} else {
		// If no placeholder, send a new reply
		replyMsg, err = sp.ctx.Bot().Reply(sp.ctx.Message(), formattedResponse, formatOpt)
		if err != nil {
			log.Error("Failed to send reply", zap.Error(err))
			return nil, err
		}
	}

	// Store the message to Redis
	if storeErr := orm.PushMessageToStream(replyMsg); storeErr != nil {
		log.Warn("Store bot's reply message to Redis failed", zap.Error(storeErr))
	}

	return replyMsg, nil
}

// handleToolCalls processes MCP tool calls and returns the updated messages
func (sp *streamProcessor) handleToolCalls(choice openai.ChatCompletionStreamChoice) error {
	// Build the complete message from the stream so far
	sp.mu.RLock()
	content := sp.fullResponse.String()
	sp.mu.RUnlock()

	completeMsg := openai.ChatCompletionMessage{
		Role:      openai.ChatMessageRoleAssistant,
		Content:   content,
		ToolCalls: choice.Delta.ToolCalls,
	}

	// Process tool calls similar to the original implementation
	*sp.messages = append(*sp.messages, completeMsg)
	for _, toolCall := range choice.Delta.ToolCalls {
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
	for {
		response, err := currentStream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}

		if len(response.Choices) == 0 {
			continue
		}

		choice := response.Choices[0]

		// Handle tool calls if MCP is enabled
		if choice.FinishReason == openai.FinishReasonToolCalls && sp.useMcp {
			newStream, err := sp.handleToolCallsInStream(choice)
			if err != nil {
				return "", err
			}
			// 关闭当前的流并将新的流设置为当前流
			if err := currentStream.Close(); err != nil {
				log.Error("Failed to close current stream", zap.Error(err))
			}
			currentStream = newStream
			continue
		}

		// Accumulate the content
		sp.processStreamChunk(choice)
	}

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
