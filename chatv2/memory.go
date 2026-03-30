//go:build !386 && !arm

package chatv2

import (
	"csust-got/chat"
	"csust-got/orm"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

const defaultHistoryContext = 10

// LoadHistory retrieves both the rendered text context and the underlying
// Telegram messages so chatv2 can recover images for multimodal input.
func LoadHistory(bot *tb.Bot, msg *tb.Message, maxContext int) (*RichHistory, error) {
	if maxContext <= 0 {
		maxContext = defaultHistoryContext
	}

	contextMsgs, err := chat.GetMessageContext(bot, msg, maxContext)
	if err != nil {
		return nil, err
	}

	return &RichHistory{
		ContextMessages: contextMsgs,
		FullMessages:    loadFullContextMessages(msg, contextMsgs),
	}, nil
}

func loadFullContextMessages(current *tb.Message, contextMsgs []*chat.ContextMessage) []*tb.Message {
	if current == nil || current.Chat == nil || len(contextMsgs) == 0 {
		return nil
	}

	liveMessages := make(map[int]*tb.Message, len(contextMsgs))
	collectReplyChainMessages(current.ReplyTo, liveMessages)

	fullMessages := make([]*tb.Message, 0, len(contextMsgs))
	for _, contextMsg := range contextMsgs {
		if contextMsg == nil {
			fullMessages = append(fullMessages, nil)
			continue
		}

		if msg, ok := liveMessages[contextMsg.ID]; ok {
			fullMessages = append(fullMessages, msg)
			continue
		}

		msg, err := loadStoredTelegramMessage(current.Chat.ID, contextMsg.ID)
		if err != nil {
			zap.L().Debug("chatv2: failed to load full context message",
				zap.Int64("chat_id", current.Chat.ID),
				zap.Int("message_id", contextMsg.ID),
				zap.Error(err),
			)
			fullMessages = append(fullMessages, nil)
			continue
		}
		fullMessages = append(fullMessages, msg)
	}

	return fullMessages
}

func collectReplyChainMessages(msg *tb.Message, messages map[int]*tb.Message) {
	visited := make(map[int]struct{})
	for current := msg; current != nil; current = current.ReplyTo {
		if _, ok := visited[current.ID]; ok {
			return
		}
		visited[current.ID] = struct{}{}
		messages[current.ID] = current
	}
}

// SaveResponse stores the bot's response and metadata to Redis for future context.
func SaveResponse(botMsg *tb.Message, userMsg *tb.Message) {
	if botMsg == nil {
		return
	}

	// Store the bot's response message
	if err := orm.SetMessage(botMsg); err != nil {
		zap.L().Error("chatv2: failed to store response message",
			zap.Error(err),
			zap.Int64("chat_id", botMsg.Chat.ID),
			zap.Int("msg_id", botMsg.ID),
		)
	}

	// Push to the chat's message stream for future context retrieval
	if err := orm.PushMessageToStream(botMsg); err != nil {
		zap.L().Error("chatv2: failed to push response to stream",
			zap.Error(err),
			zap.Int64("chat_id", botMsg.Chat.ID),
			zap.Int("msg_id", botMsg.ID),
		)
	}
}
