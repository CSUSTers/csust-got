package chatv2

import (
	"csust-got/chat"
	"csust-got/orm"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// LoadHistory retrieves conversation history for the current message.
// It reuses the existing chat.GetMessageContext which handles:
// - Reply chain reconstruction
// - Previous messages from Redis stream
// - Entity-aware text extraction
func LoadHistory(bot *tb.Bot, msg *tb.Message, maxContext int) ([]*chat.ContextMessage, error) {
	if maxContext <= 0 {
		maxContext = 10
	}
	return chat.GetMessageContext(bot, msg, maxContext)
}

// SaveResponse stores the bot's response and metadata to Redis for future context.
func SaveResponse(botMsg *tb.Message, userMsg *tb.Message) {
	if botMsg == nil {
		return
	}

	// Store the bot's response message
	orm.SetMessage(botMsg)

	// Push to the chat's message stream for future context retrieval
	if err := orm.PushMessageToStream(botMsg); err != nil {
		zap.L().Error("chatv2: failed to push response to stream",
			zap.Error(err),
			zap.Int64("chat_id", botMsg.Chat.ID),
			zap.Int("msg_id", botMsg.ID),
		)
	}
}
