//go:build !386 && !arm

package chatv2

import (
	"context"
	"csust-got/config"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

var errNoCompiledConfig = errors.New("no compiled config found")

// compiledChats stores pre-compiled chat configurations, keyed by chat config name.
var (
	compiledChats sync.Map // map[string]*CompiledChat
	mcpManager    *McpManager
)

// Init compiles all agent-enabled chat configurations at startup.
// Must be called after config is loaded and before bot starts.
func Init(ctx context.Context) error {
	mcpManager = NewMcpManager()

	if config.BotConfig.ChatConfigV2 == nil {
		return nil
	}

	for _, chatCfg := range *config.BotConfig.ChatConfigV2 {
		if !chatCfg.IsAgentEnabled() {
			continue
		}

		compiled, err := CompileChat(ctx, chatCfg, mcpManager)
		if err != nil {
			zap.L().Error("chatv2: failed to compile chat config",
				zap.String("name", chatCfg.Name),
				zap.Error(err),
			)
			continue // graceful degradation: skip failed configs
		}

		compiledChats.Store(chatCfg.Name, compiled)
		zap.L().Info("chatv2: compiled chat config",
			zap.String("name", chatCfg.Name),
		)
	}

	return nil
}

// Close shuts down all chatv2 resources.
func Close() {
	if mcpManager != nil {
		mcpManager.Close()
	}
}

// Chat is the main handler function for chatv2.
// Signature matches chat.Chat() for compatibility with the bot's handler registration.
func Chat(tbCtx tb.Context, chatCfg *config.ChatConfigSingle, trigger *config.ChatTrigger) error {
	// Look up pre-compiled chat
	val, ok := compiledChats.Load(chatCfg.Name)
	if !ok {
		return fmt.Errorf("chatv2: %w for %q", errNoCompiledConfig, chatCfg.Name)
	}
	compiled := val.(*CompiledChat)

	msg := tbCtx.Message()
	if msg == nil {
		return nil
	}

	// Run filters
	if !ProcessFilters(tbCtx, chatCfg) {
		return nil
	}

	// Extract user input
	input := extractInput(msg, trigger)
	if input == "" {
		return nil
	}

	// Create turn context
	ctx, cancel := context.WithTimeout(context.Background(), chatCfg.GetTimeout())
	defer cancel()

	tc := &TurnContext{
		Bot:     tbCtx.Bot(),
		Message: msg,
		ChatID:  msg.Chat.ID,
		Config:  chatCfg,
		Trigger: trigger,
		BotUser: tbCtx.Bot().Me,
	}
	ctx = WithTurnContext(ctx, tc)

	// Load conversation history
	history, err := LoadHistory(tc.Bot, msg, chatCfg.MessageContext)
	if err != nil {
		zap.L().Warn("chatv2: failed to load history", zap.Error(err))
		// Continue without history — pass nil
	}

	// Build messages for the agent
	messages, err := BuildMessages(compiled, tc, history)
	if err != nil {
		zap.L().Error("chatv2: failed to build messages", zap.Error(err))
		return sendErrorMessage(tbCtx, chatCfg)
	}

	// Send typing indicator
	_ = tbCtx.Bot().Notify(tbCtx.Chat(), tb.Typing)

	// Execute agent
	if chatCfg.Format.StreamOutput {
		return handleStreaming(ctx, tbCtx, compiled, messages, chatCfg)
	}
	return handleNonStreaming(ctx, tbCtx, compiled, messages, chatCfg)
}

// handleStreaming processes the agent response with streaming output.
func handleStreaming(
	ctx context.Context,
	tbCtx tb.Context,
	compiled *CompiledChat,
	messages []*schema.Message,
	chatCfg *config.ChatConfigSingle,
) error {
	// Get placeholder text
	placeholder := chatCfg.PlaceHolder
	if placeholder == "" {
		placeholder = "..."
	}

	// Stream from agent
	reader, err := compiled.Agent.Stream(ctx, messages)
	if err != nil {
		zap.L().Error("chatv2: agent stream failed", zap.Error(err))
		return sendErrorMessage(tbCtx, chatCfg)
	}

	// Stream to Telegram
	response, _, sentMsg, streamErr := StreamToTelegram(ctx, tbCtx, reader, &chatCfg.Format, placeholder)
	if streamErr != nil {
		zap.L().Error("chatv2: streaming failed", zap.Error(streamErr))
	}
	// Save response to Redis for future context
	if response != "" && sentMsg != nil {
		sentMsg.Text = response
		SaveResponse(sentMsg, tbCtx.Message())
	}

	return nil
}

// handleNonStreaming processes the agent response without streaming.
func handleNonStreaming(
	ctx context.Context,
	tbCtx tb.Context,
	compiled *CompiledChat,
	messages []*schema.Message,
	chatCfg *config.ChatConfigSingle,
) error {
	result, err := compiled.Agent.Generate(ctx, messages)
	if err != nil {
		zap.L().Error("chatv2: agent generate failed", zap.Error(err))
		return sendErrorMessage(tbCtx, chatCfg)
	}

	response := result.Content
	reasoning := result.ReasoningContent

	sent, sendErr := NonStreamResponse(tbCtx, response, reasoning, &chatCfg.Format)
	if sendErr != nil {
		zap.L().Error("chatv2: failed to send response", zap.Error(sendErr))
		return sendErr
	}

	if sent != nil {
		SaveResponse(sent, tbCtx.Message())
	}

	return nil
}

// sendErrorMessage sends the configured error message to the user.
func sendErrorMessage(tbCtx tb.Context, chatCfg *config.ChatConfigSingle) error {
	errMsg := chatCfg.GetErrorMessage()
	if errMsg == "" {
		errMsg = "抱歉，处理请求时发生错误。"
	}
	return tbCtx.Reply(errMsg)
}
