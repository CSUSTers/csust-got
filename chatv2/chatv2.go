//go:build !386 && !arm

package chatv2

import (
	"context"
	"csust-got/config"
	"csust-got/util"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/compose"
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
	if err := validateAgentV3StartupConfig(); err != nil {
		return err
	}

	mcpManager = NewMcpManager()

	if config.BotConfig == nil || config.BotConfig.Agents == nil || len(*config.BotConfig.Agents) == 0 {
		return nil
	}

	for _, chatCfg := range *config.BotConfig.Agents {
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

func validateAgentV3StartupConfig() error {
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil || config.BotConfig.Agents == nil {
		return nil
	}

	for _, chatCfg := range *config.BotConfig.Agents {
		if !chatCfg.IsAgentV3Enabled() {
			continue
		}
		if err := validateAgentV3RuntimeConfig(config.BotConfig.AgentV3); err != nil {
			return fmt.Errorf("chatv2: agent v3 chat %q cannot use runtime: %w", chatCfg.Name, err)
		}
	}

	return nil
}

// HasCompiledChat reports whether a compiled chat config exists for the given name.
func HasCompiledChat(name string) bool {
	_, ok := compiledChats.Load(name)
	return ok
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

	history, err := LoadHistory(tc.Bot, msg, chatCfg.MessageContext)
	if err != nil {
		zap.L().Warn("chatv2: failed to load history", zap.Error(err))
		history = &RichHistory{}
	}

	var messages []*schema.Message
	if chatCfg.IsAgentV3Enabled() {
		messages, err = prepareAgentV3Turn(ctx, compiled, tc, history)
		if err != nil {
			if tc.V3 != nil && tc.V3.Trace != nil {
				tc.V3.Trace.SetError(err)
				tc.V3.Trace.Finish(ctx, tc.V3.Scope)
			}
			zap.L().Error("chatv2: failed to build agent v3 messages", zap.Error(err))
			return sendAgentErrorMessage(tbCtx, chatCfg, err)
		}
		defer tc.V3.Trace.Finish(ctx, tc.V3.Scope)
		if err := maybeRememberExplicitInput(ctx, tc, input); err != nil {
			tc.V3.Trace.SetError(err)
			zap.L().Warn("chatv2: failed to write agent v3 explicit memory",
				zap.String("run_id", tc.RunID),
				zap.Error(err),
			)
		}
		zap.L().Info("chatv2: agent v3 turn started",
			zap.String("run_id", tc.RunID),
			zap.String("agent", chatCfg.Name),
			zap.Int64("chat_id", tc.ChatID),
		)
	} else {
		// Build messages for the agent
		messages, err = BuildMessages(compiled, tc, history)
		if err != nil {
			zap.L().Error("chatv2: failed to build messages", zap.Error(err))
			return sendAgentErrorMessage(tbCtx, chatCfg, err)
		}
	}

	// Send typing indicator
	_ = tbCtx.Bot().Notify(tbCtx.Chat(), tb.Typing)

	if ps := chatCfg.Format.ProgressSummary; ps != nil && ps.Enable {
		ph := chatCfg.PlaceHolder
		if ph == "" {
			ph = "..."
		}
		placeholderMsg, phErr := util.SendMessageWithError(tbCtx.Chat(), ph, &tb.SendOptions{
			ReplyTo: msg,
		})
		if phErr != nil {
			zap.L().Warn("chatv2: failed to send progress placeholder", zap.Error(phErr))
		} else {
			tc.SetProgressMsg(placeholderMsg)
		}
	}
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
	tc := GetTurnContext(ctx)
	reader, err := compiled.Agent.Stream(ctx, messages)
	if err != nil {
		if tc != nil && tc.V3 != nil && tc.V3.Trace != nil {
			tc.V3.Trace.SetError(err)
		}
		zap.L().Error("chatv2: agent stream failed", zap.Error(err))
		return sendAgentErrorMessage(tbCtx, chatCfg, err)
	}

	tc.streamingStarted.Store(true)
	response, _, sentMsg, streamErr := StreamToTelegram(ctx, tbCtx, reader, &chatCfg.Format, tc.GetProgressMsg(), chatCfg.IsAgentV3RichEnabled())
	if streamErr != nil {
		if tc != nil && tc.V3 != nil && tc.V3.Trace != nil {
			tc.V3.Trace.SetError(streamErr)
		}
		zap.L().Error("chatv2: streaming failed", zap.Error(streamErr))
		if response == "" {
			return sendAgentErrorMessage(tbCtx, chatCfg, streamErr)
		}
		return streamErr
	}
	// Save response to Redis for future context
	if response != "" && sentMsg != nil {
		sentMsg.Text = response
		SaveResponse(sentMsg, tbCtx.Message())
		if err := saveAgentV3TurnPair(ctx, tc, extractInput(tbCtx.Message(), tc.Trigger), response, sentMsg.ID); err != nil {
			if tc != nil && tc.V3 != nil && tc.V3.Trace != nil {
				tc.V3.Trace.SetError(err)
			}
			zap.L().Warn("chatv2: failed to save agent v3 turn", zap.Error(err))
		}
	}

	return streamErr
}

// handleNonStreaming processes the agent response without streaming.
func handleNonStreaming(
	ctx context.Context,
	tbCtx tb.Context,
	compiled *CompiledChat,
	messages []*schema.Message,
	chatCfg *config.ChatConfigSingle,
) error {
	tc := GetTurnContext(ctx)
	result, err := compiled.Agent.Generate(ctx, messages)
	if err != nil {
		if tc != nil && tc.V3 != nil && tc.V3.Trace != nil {
			tc.V3.Trace.SetError(err)
		}
		zap.L().Error("chatv2: agent generate failed", zap.Error(err))
		return sendAgentErrorMessage(tbCtx, chatCfg, err)
	}
	response := result.Content
	reasoning := result.ReasoningContent

	tc.streamingStarted.Store(true)

	sent, visibleResponse, sendErr := NonStreamResponse(tbCtx, response, reasoning, &chatCfg.Format, tc.GetProgressMsg(), chatCfg.IsAgentV3RichEnabled(), tc.richMessageSkillLoadedForFinal())
	if sendErr != nil {
		if tc != nil && tc.V3 != nil && tc.V3.Trace != nil {
			tc.V3.Trace.SetError(sendErr)
		}
		zap.L().Error("chatv2: failed to send response", zap.Error(sendErr))
		return sendErr
	}

	if sent != nil {
		sent.Text = visibleResponse
		SaveResponse(sent, tbCtx.Message())
		if err := saveAgentV3TurnPair(ctx, tc, extractInput(tbCtx.Message(), tc.Trigger), visibleResponse, sent.ID); err != nil {
			if tc != nil && tc.V3 != nil && tc.V3.Trace != nil {
				tc.V3.Trace.SetError(err)
			}
			zap.L().Warn("chatv2: failed to save agent v3 turn", zap.Error(err))
		}
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

func sendAgentErrorMessage(tbCtx tb.Context, chatCfg *config.ChatConfigSingle, err error) error {
	msg := friendlyAgentErrorMessage(err)
	if msg == "" {
		return sendErrorMessage(tbCtx, chatCfg)
	}
	return tbCtx.Reply(msg)
}

func friendlyAgentErrorMessage(err error) string {
	if err == nil {
		return ""
	}

	if errors.Is(err, compose.ErrExceedMaxSteps) {
		return "这次处理卡在 agent 的步骤上限了：它已经跑完了可用轮次，但还没来得及收束成最终答案。请稍后重试；如果这是搜索或总结类 bot，通常需要把对应配置里的 max_steps 调高。"
	}

	if msg, ok := recoverableImageToolMessage(err); ok {
		return msg
	}

	if strings.Contains(err.Error(), "node path: [tools]") {
		return "工具调用阶段遇到错误，请稍后重试。"
	}

	return "回答生成阶段遇到错误，请稍后重试。"
}
