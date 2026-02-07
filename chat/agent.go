package chat

import (
	"context"
	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"strings"
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/tools"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// agentProcessor handles agent-based chat execution with multi-round tool calling
// using langchaingo as the LLM framework.
type agentProcessor struct {
	chatCtx context.Context
	ctx     tb.Context
	model   llms.Model
	config  *config.ChatConfigSingle

	// Tool definitions and callable adapters
	toolDefs     []llms.Tool
	toolAdapters map[string]tools.Tool

	// Messages accumulated during conversation
	messages []llms.MessageContent

	// Streaming state
	placeholderMsg *tb.Message
	fullResponse   strings.Builder
	reasonContent  strings.Builder
	lastSentText   string
	ticker         *time.Ticker
	done           chan struct{}
	stopOnce       sync.Once
	mu             sync.RWMutex
}

// newAgentProcessor creates a new agentProcessor for handling agent-based chat.
func newAgentProcessor(
	chatCtx context.Context,
	ctx tb.Context,
	model llms.Model,
	chatConfig *config.ChatConfigSingle,
	messages []llms.MessageContent,
	placeholderMsg *tb.Message,
) *agentProcessor {
	ap := &agentProcessor{
		chatCtx:        chatCtx,
		ctx:            ctx,
		model:          model,
		config:         chatConfig,
		messages:       messages,
		placeholderMsg: placeholderMsg,
		done:           make(chan struct{}),
	}

	// Set up tools if MCP is enabled
	if chatConfig.UseMcpo && config.BotConfig.McpoServer.Enable {
		ap.toolDefs = GetLangchainTools("")
		ap.toolAdapters = GetLangchainToolAdapters("")
	}

	return ap
}

// run executes the agent loop with multi-round tool calling support.
func (ap *agentProcessor) run() (string, error) {
	maxIterations := ap.config.Agent.GetMaxIterations()

	// Start streaming ticker if enabled
	ap.startStreamingTicker()
	defer ap.stopTicker()

	for iteration := range maxIterations {
		log.Debug("agent iteration",
			zap.Int("iteration", iteration),
			zap.Int("maxIterations", maxIterations))

		// Build call options
		opts := ap.buildCallOptions()

		// Call the model
		resp, err := ap.model.GenerateContent(ap.chatCtx, ap.messages, opts...)
		if err != nil {
			log.Error("agent: model GenerateContent failed", zap.Error(err))
			return "", err
		}

		if len(resp.Choices) == 0 {
			log.Warn("agent: empty response from model")
			break
		}

		choice := resp.Choices[0]

		// Accumulate content
		if choice.Content != "" {
			ap.mu.Lock()
			ap.fullResponse.WriteString(choice.Content)
			ap.mu.Unlock()
		}

		// Accumulate reasoning content
		if choice.ReasoningContent != "" {
			ap.mu.Lock()
			ap.reasonContent.WriteString(choice.ReasoningContent)
			ap.mu.Unlock()
		}

		// Check for tool calls
		if len(choice.ToolCalls) > 0 {
			// Add assistant message with tool calls to conversation
			assistantMsg := llms.MessageContent{
				Role:  llms.ChatMessageTypeAI,
				Parts: []llms.ContentPart{},
			}
			if choice.Content != "" {
				assistantMsg.Parts = append(assistantMsg.Parts, llms.TextPart(choice.Content))
			}
			for _, tc := range choice.ToolCalls {
				assistantMsg.Parts = append(assistantMsg.Parts, tc)
			}
			ap.messages = append(ap.messages, assistantMsg)

			// Execute tool calls
			if err := ap.executeToolCalls(choice.ToolCalls); err != nil {
				log.Error("agent: tool execution failed", zap.Error(err))
				return "", err
			}

			// Send typing notification
			_ = ap.ctx.Bot().Notify(ap.ctx.Chat(), tb.Typing)

			// Reset response buffer for next iteration
			ap.mu.Lock()
			ap.fullResponse.Reset()
			ap.reasonContent.Reset()
			ap.mu.Unlock()
			ap.lastSentText = ""

			continue
		}

		// No tool calls - we have a final response
		// Add the final assistant message
		ap.messages = append(ap.messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: []llms.ContentPart{llms.TextPart(choice.Content)},
		})

		break
	}

	// Finalize and send the response
	return ap.finalizeResponse()
}

// buildCallOptions creates langchaingo call options from the config.
func (ap *agentProcessor) buildCallOptions() []llms.CallOption {
	opts := []llms.CallOption{
		llms.WithTemperature(float64(ap.config.GetTemperature())),
	}

	// Add streaming callback
	if ap.config.Format.StreamOutput {
		opts = append(opts, llms.WithStreamingFunc(ap.streamingCallback))
	}

	// Add tools
	if len(ap.toolDefs) > 0 {
		opts = append(opts, llms.WithTools(ap.toolDefs))
	}

	return opts
}

// streamingCallback is the langchaingo streaming callback that accumulates content.
func (ap *agentProcessor) streamingCallback(_ context.Context, chunk []byte) error {
	ap.mu.Lock()
	ap.fullResponse.Write(chunk)
	ap.mu.Unlock()
	return nil
}

// executeToolCalls processes tool calls returned by the model.
func (ap *agentProcessor) executeToolCalls(toolCalls []llms.ToolCall) error {
	for _, tc := range toolCalls {
		funcName := ""
		funcArgs := ""
		if tc.FunctionCall != nil {
			funcName = tc.FunctionCall.Name
			funcArgs = tc.FunctionCall.Arguments
		}

		log.Debug("agent: executing tool call",
			zap.String("id", tc.ID),
			zap.String("function", funcName),
			zap.String("args", funcArgs))

		var result string
		adapter, ok := ap.toolAdapters[funcName]
		if !ok {
			log.Error("agent: tool not found", zap.String("name", funcName))
			result = "Tool not found: " + funcName
		} else {
			var err error
			result, err = adapter.Call(ap.chatCtx, funcArgs)
			if err != nil {
				log.Error("agent: tool call failed",
					zap.String("name", funcName), zap.Error(err))
				result = "Tool call failed: " + err.Error()
			}
		}

		log.Debug("agent: tool call result",
			zap.String("name", funcName),
			zap.String("result", result))

		// Add tool response to messages
		ap.messages = append(ap.messages, llms.MessageContent{
			Role: llms.ChatMessageTypeTool,
			Parts: []llms.ContentPart{
				llms.ToolCallResponse{
					ToolCallID: tc.ID,
					Name:       funcName,
					Content:    result,
				},
			},
		})
	}
	return nil
}

// startStreamingTicker starts the ticker for real-time message updates.
func (ap *agentProcessor) startStreamingTicker() {
	if !ap.config.Format.StreamOutput {
		return
	}

	editInterval := ap.config.Format.GetEditInterval()
	ap.ticker = time.NewTicker(editInterval)

	go func() {
		defer ap.ticker.Stop()
		for {
			select {
			case <-ap.ticker.C:
				ap.updateStreamingMessage()
			case <-ap.done:
				return
			}
		}
	}()
}

// updateStreamingMessage sends partial content updates to the Telegram message.
func (ap *agentProcessor) updateStreamingMessage() {
	ap.mu.RLock()
	currentText := ap.fullResponse.String()
	nativeReason := ap.reasonContent.String()
	ap.mu.RUnlock()

	if currentText == "" || currentText == ap.lastSentText {
		return
	}

	delimiters := config.BotConfig.SentenceDelimiters
	lastDelimEndPos := findLastSentenceDelimiter(currentText, delimiters)
	if lastDelimEndPos <= 0 {
		return
	}

	textToSend := currentText[:lastDelimEndPos]
	if textToSend == ap.lastSentText {
		return
	}

	formattedText := formatOutputWithReason(textToSend, nativeReason, &ap.config.Format)
	if formattedText == "" {
		return
	}

	formatOpt := ap.getFormatOption()
	var err error
	if ap.placeholderMsg == nil {
		ap.placeholderMsg, err = ap.ctx.Bot().Reply(ap.ctx.Message(), formattedText, formatOpt)
		if err != nil {
			log.Error("agent: failed to create initial reply during streaming", zap.Error(err))
			return
		}
	} else {
		_, err = util.EditMessageWithError(ap.placeholderMsg, formattedText, formatOpt)
		if err != nil {
			log.Error("agent: failed to edit message during streaming", zap.Error(err))
			return
		}
	}
	ap.lastSentText = textToSend
}

// stopTicker stops the streaming ticker.
func (ap *agentProcessor) stopTicker() {
	ap.stopOnce.Do(func() {
		if ap.ticker != nil {
			close(ap.done)
			ap.ticker.Stop()
		}
	})
}

// getFormatOption returns the Telegram parse mode.
func (ap *agentProcessor) getFormatOption() tb.ParseMode {
	if ap.config.Format.GetFormat() == config.OutputFormatHTML {
		return tb.ModeHTML
	}
	return tb.ModeMarkdownV2
}

// finalizeResponse sends the final formatted response.
func (ap *agentProcessor) finalizeResponse() (string, error) {
	ap.stopTicker()

	ap.mu.RLock()
	finalResponse := strings.TrimSpace(ap.fullResponse.String())
	nativeReason := strings.TrimSpace(ap.reasonContent.String())
	ap.mu.RUnlock()

	formattedResponse := formatOutputWithReason(finalResponse, nativeReason, &ap.config.Format)
	if formattedResponse == "" {
		log.Warn("agent: final response is empty, sending error message")
		formattedResponse = ap.config.GetErrorMessage()
	}

	formatOpt := ap.getFormatOption()
	var replyMsg *tb.Message
	var err error

	if ap.placeholderMsg != nil {
		replyMsg, err = util.EditMessageWithError(ap.placeholderMsg, formattedResponse, formatOpt)
		if err != nil {
			log.Error("agent: failed to edit placeholder with final response", zap.Error(err))
			return "", err
		}
	} else {
		replyMsg, err = ap.ctx.Bot().Reply(ap.ctx.Message(), formattedResponse, formatOpt)
		if err != nil {
			log.Error("agent: failed to send reply", zap.Error(err))
			return "", err
		}
	}

	// Store message to Redis
	if storeErr := orm.PushMessageToStream(replyMsg); storeErr != nil {
		log.Warn("agent: store reply to Redis stream failed", zap.Error(storeErr))
	}
	if storeErr := orm.SetMessage(replyMsg); storeErr != nil {
		log.Warn("agent: store reply to Redis failed", zap.Error(storeErr))
	}

	return finalResponse, nil
}

// convertToLangchainMessages converts the internal message format to langchaingo MessageContent.
func convertToLangchainMessages(systemPrompt string, userPrompt string, multiPartContent bool, imageURL string) []llms.MessageContent {
	messages := make([]llms.MessageContent, 0)

	if systemPrompt != "" {
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(systemPrompt)},
		})
	}

	if multiPartContent && imageURL != "" {
		messages = append(messages, llms.MessageContent{
			Role: llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{
				llms.ImageURLPart(imageURL),
				llms.TextPart(userPrompt),
			},
		})
	} else {
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(userPrompt)},
		})
	}

	return messages
}
