package chat

import (
	"context"
	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"fmt"
	"sync"

	"github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// regeneratingMessages tracks messages currently being regenerated to prevent concurrent regeneration
var regeneratingMessages = struct {
	sync.Mutex
	msgs map[string]bool
}{
	msgs: make(map[string]bool),
}

// isRegenerating checks if a message is currently being regenerated
func isRegenerating(chatID int64, messageID int) bool {
	regeneratingMessages.Lock()
	defer regeneratingMessages.Unlock()
	key := makeRegeneratingKey(chatID, messageID)
	return regeneratingMessages.msgs[key]
}

// setRegenerating marks a message as being regenerated
func setRegenerating(chatID int64, messageID int, value bool) {
	regeneratingMessages.Lock()
	defer regeneratingMessages.Unlock()
	key := makeRegeneratingKey(chatID, messageID)
	if value {
		regeneratingMessages.msgs[key] = true
	} else {
		delete(regeneratingMessages.msgs, key)
	}
}

// makeRegeneratingKey creates a unique key for tracking regenerating messages
func makeRegeneratingKey(chatID int64, messageID int) string {
	return fmt.Sprintf("%d:%d", chatID, messageID)
}

// HandleMessageReaction handles user reactions to bot messages
// When a user reacts with 👎 to an AI-generated message, regenerate the response
func HandleMessageReaction(ctx tb.Context) error {
	reaction := ctx.Update().MessageReaction
	if reaction == nil {
		return nil
	}

	// Check if it's a 👎 reaction
	hasThumbsDown := false
	for _, r := range reaction.NewReaction {
		if r.Emoji == "👎" {
			hasThumbsDown = true
			break
		}
	}

	if !hasThumbsDown {
		return nil
	}

	// Check if this message is already being regenerated
	if isRegenerating(reaction.Chat.ID, reaction.MessageID) {
		log.Debug("Message already being regenerated, skipping",
			zap.Int64("chat", reaction.Chat.ID),
			zap.Int("msg", reaction.MessageID))
		return nil
	}

	// Mark as regenerating and ensure cleanup on exit
	setRegenerating(reaction.Chat.ID, reaction.MessageID, true)
	defer setRegenerating(reaction.Chat.ID, reaction.MessageID, false)

	// Get the bot message metadata
	metadata, err := orm.GetAIResponseMetadata(reaction.Chat.ID, reaction.MessageID)
	if err != nil {
		// Not an AI-generated message or metadata not found
		log.Debug("AI response metadata not found",
			zap.Int64("chat", reaction.Chat.ID),
			zap.Int("msg", reaction.MessageID),
			zap.Error(err))
		return nil
	}

	// Find the chat config by name using O(1) map lookup
	chatConfig, ok := chatConfigs.Load(metadata.ConfigName)
	if !ok {
		log.Error("Chat config not found", zap.String("configName", metadata.ConfigName))
		return nil
	}

	// Check if regeneration is allowed based on stored metadata flag
	// If AllowRegenerate is nil (not set), fall back to config setting for backward compatibility
	if metadata.AllowRegenerate != nil && !*metadata.AllowRegenerate {
		log.Debug("Regeneration not allowed for this message",
			zap.Int64("chat", reaction.Chat.ID),
			zap.Int("msg", reaction.MessageID))
		return nil
	}

	// Limit regeneration attempts
	if metadata.RegenerateCount >= chatConfig.Features.GetMaxRegenerateCount() {
		log.Info("Max regeneration count reached",
			zap.Int64("chat", reaction.Chat.ID),
			zap.Int("msg", reaction.MessageID))
		return nil
	}

	log.Info("Regenerating response due to 👎 reaction",
		zap.Int64("chat", reaction.Chat.ID),
		zap.Int("msg", reaction.MessageID),
		zap.Int("userMsg", metadata.UserMessageID))

	// Get the original user message
	userMsg, err := orm.GetMessage(reaction.Chat.ID, metadata.UserMessageID)
	if err != nil {
		log.Error("Failed to get original user message",
			zap.Int64("chat", reaction.Chat.ID),
			zap.Int("userMsg", metadata.UserMessageID),
			zap.Error(err))
		return nil
	}

	// Get the bot message that needs to be edited
	botMsg, err := orm.GetMessage(reaction.Chat.ID, reaction.MessageID)
	if err != nil {
		log.Error("Failed to get bot message",
			zap.Int64("chat", reaction.Chat.ID),
			zap.Int("botMsg", reaction.MessageID),
			zap.Error(err))
		return nil
	}

	// If AllowRegenerate is nil (legacy metadata), fall back to config setting
	if metadata.AllowRegenerate == nil && !chatConfig.Features.AllowRegenerate {
		log.Debug("Regeneration disabled in config for this chat",
			zap.String("configName", metadata.ConfigName))
		return nil
	}

	// Update original message with processing emoji to indicate regeneration is in progress
	parseMode := getParseMode(chatConfig)
	processingPrefix := "⏳ "
	botMsgContent := botMsg.Text
	if botMsgContent == "" {
		botMsgContent = botMsg.Caption
	}
	_, editErr := util.EditMessageWithError(botMsg, util.RawTgText(processingPrefix+botMsgContent), parseMode)
	if editErr != nil {
		log.Warn("Failed to add processing indicator to message", zap.Error(editErr))
	}

	// Create a regeneration context
	// Add the previous bot response and user feedback to messages
	regenerateMessages := metadata.Messages
	if regenerateMessages == nil {
		regenerateMessages = make([]openai.ChatCompletionMessage, 0)
	}

	// Get bot message content (prefer Text, fallback to Caption)
	assistantContent := botMsg.Text
	if assistantContent == "" {
		assistantContent = botMsg.Caption
	}

	regenerateMessages = append(regenerateMessages,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: assistantContent,
		},
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: chatConfig.Features.GetRegenerateFeedback(),
		},
	)

	// Regenerate with the updated context
	err = regenerateResponse(ctx.Bot(), userMsg, botMsg, chatConfig, regenerateMessages, metadata.RegenerateCount+1)
	if err != nil {
		log.Error("Failed to regenerate response",
			zap.Int64("chat", reaction.Chat.ID),
			zap.Int("botMsg", reaction.MessageID),
			zap.Error(err))
		// Restore original message content by removing the processing indicator
		_, restoreErr := util.EditMessageWithError(botMsg, util.RawTgText(botMsgContent), parseMode)
		if restoreErr != nil {
			log.Warn("Failed to restore original message after regeneration failure", zap.Error(restoreErr))
		}
		return err
	}

	return nil
}

// regenerateResponse regenerates an AI response and edits the original message
func regenerateResponse(bot *tb.Bot, userMsg, botMsg *tb.Message, chatConfig *config.ChatConfigSingle,
	messages []openai.ChatCompletionMessage, regenerateCount int) error {

	client := clients[chatConfig.Model.Name]
	if client == nil {
		log.Error("AI client not found", zap.String("model", chatConfig.Model.Name))
		return orm.ErrClientNotFound
	}

	// Notify user that we're regenerating
	err := bot.Notify(botMsg.Chat, tb.Typing)
	if err != nil {
		log.Warn("Failed to send typing notification", zap.Error(err))
	}

	// Create chat completion request
	useMcp := chatConfig.UseMcpo && config.BotConfig.McpoServer.Enable
	request := openai.ChatCompletionRequest{
		Model:           chatConfig.Model.Model,
		Messages:        messages,
		Temperature:     chatConfig.GetTemperature(),
		Stream:          true,
		ReasoningEffort: chatConfig.ReasoningEffort,
	}
	if useMcp {
		request.Tools = mcpo.GetToolSet("")
	}

	// Create streaming context
	chatCtx, cancel := context.WithTimeout(context.Background(), chatConfig.GetTimeout())
	defer cancel()

	stream, err := client.CreateChatCompletionStream(chatCtx, request)
	if err != nil {
		log.Error("Failed to create chat completion stream for regeneration", zap.Error(err))
		// Update message with error
		_, editErr := util.EditMessageWithError(botMsg, chatConfig.GetErrorMessage(), getParseMode(chatConfig))
		if editErr != nil {
			log.Error("Failed to edit message with error", zap.Error(editErr))
		}
		return err
	}

	// Create a mock context for the regeneration
	// We need to create a Context that has the user message
	mockUpdate := tb.Update{
		Message: userMsg,
	}
	mockCtx := bot.NewContext(mockUpdate)

	// Process the streaming response
	processor := newStreamProcessor(chatCtx, mockCtx, botMsg, useMcp, &request, &messages, chatConfig)
	response, err := processor.process(stream)
	if err != nil {
		log.Error("Failed to process streaming response for regeneration", zap.Error(err))
		_, editErr := util.EditMessageWithError(botMsg, chatConfig.GetErrorMessage(), getParseMode(chatConfig))
		if editErr != nil {
			log.Error("Failed to edit message with error", zap.Error(editErr))
		}
		return err
	}

	// Update metadata with new regenerate count
	originalPrompt := userMsg.Text
	if originalPrompt == "" {
		originalPrompt = userMsg.Caption
	}

	allowRegenerate := chatConfig.Features.AllowRegenerate
	metadata := &orm.AIResponseMetadata{
		BotMessageID:    botMsg.ID,
		UserMessageID:   userMsg.ID,
		ChatID:          botMsg.Chat.ID,
		ConfigName:      chatConfig.Name,
		OriginalPrompt:  originalPrompt,
		Messages:        messages,
		RegenerateCount: regenerateCount,
		AllowRegenerate: &allowRegenerate,
	}
	if err := orm.SetAIResponseMetadata(metadata); err != nil {
		log.Warn("Failed to update AI response metadata after regeneration", zap.Error(err))
	}

	log.Info("Successfully regenerated response",
		zap.Int64("chat", botMsg.Chat.ID),
		zap.Int("botMsg", botMsg.ID),
		zap.String("response", response))

	return nil
}

func getParseMode(chatConfig *config.ChatConfigSingle) tb.ParseMode {
	if chatConfig.Format.GetFormat() == config.OutputFormatHTML {
		return tb.ModeHTML
	}
	return tb.ModeMarkdownV2
}
