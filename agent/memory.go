package agentv3

import (
	"context"
	"csust-got/orm"
	"errors"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

const defaultHistoryContext = 10

// LoadHistory retrieves both the rendered text context and the underlying
// Telegram messages so agent v3 can recover images for multimodal input.
func LoadHistory(bot *tb.Bot, msg *tb.Message, maxContext int) (*RichHistory, error) {
	if maxContext <= 0 {
		maxContext = defaultHistoryContext
	}

	contextMsgs, err := GetMessageContext(bot, msg, maxContext)
	if err != nil {
		return nil, err
	}

	return &RichHistory{
		ContextMessages: contextMsgs,
		FullMessages:    loadFullContextMessages(msg, contextMsgs),
	}, nil
}

func loadFullContextMessages(current *tb.Message, contextMsgs []*ContextMessage) []*tb.Message {
	if current == nil || current.Chat == nil || len(contextMsgs) == 0 {
		return nil
	}
	ctx, done := withContextMessageResolver(context.Background())
	defer done()

	liveMessages := make(map[int]*tb.Message, len(contextMsgs))
	collectReplyChainMessages(current.ReplyTo, liveMessages)
	candidates := make([]*tb.Message, 0, len(contextMsgs))
	for _, message := range contextMsgs {
		if message == nil {
			continue
		}
		candidate := liveMessages[message.ID]
		if candidate == nil {
			candidate = &tb.Message{ID: message.ID, Chat: current.Chat}
		}
		candidates = append(candidates, candidate)
	}
	if err := ctx.Value(contextMessageResolverKey{}).(*contextMessageResolver).prime(current.Chat.ID, candidates...); err != nil {
		return make([]*tb.Message, len(contextMsgs))
	}

	fullMessages := make([]*tb.Message, 0, len(contextMsgs))
	for _, contextMsg := range contextMsgs {
		if contextMsg == nil {
			fullMessages = append(fullMessages, nil)
			continue
		}

		candidate := liveMessages[contextMsg.ID]
		if candidate == nil {
			candidate = &tb.Message{ID: contextMsg.ID, Chat: current.Chat}
		}
		resolved, process, err := resolveContextMessage(ctx, current.Chat.ID, candidate)
		if err != nil || process {
			fullMessages = append(fullMessages, nil)
			continue
		}
		if resolved != candidate || liveMessages[contextMsg.ID] != nil {
			fullMessages = append(fullMessages, resolved)
			continue
		}

		msg, err := loadStoredTelegramMessage(current.Chat.ID, contextMsg.ID)
		if err != nil {
			zap.L().Debug("agentv3: failed to load full context message",
				zap.Int64("chat_id", current.Chat.ID),
				zap.Int("message_id", contextMsg.ID),
				zap.Error(err),
			)
			fullMessages = append(fullMessages, nil)
			continue
		}
		resolved, process, err = resolveContextMessage(ctx, current.Chat.ID, msg)
		if err != nil || process {
			resolved = nil
		}
		fullMessages = append(fullMessages, resolved)
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

// SaveResponse stores the delivered bot reply so GetMessage and the chat stream can serve it as reply-chain context.
func SaveResponse(botMsg *tb.Message, userMsg *tb.Message) error {
	if botMsg == nil || botMsg.Chat == nil {
		return nil
	}
	stored := responseWithParent(botMsg, userMsg)
	var finalErr error
	if stored.Sender != nil && stored.Sender.IsBot {
		finalErr = orm.StoreBotMessageLifecycle(stored, stored.Sender.ID, true)
	}
	if err := errors.Join(finalErr, orm.SetMessage(stored), orm.PushMessageToStream(stored)); err != nil {
		zap.L().Error("agentv3: failed to store response message",
			zap.Error(err),
			zap.Int64("chat_id", stored.Chat.ID),
			zap.Int("msg_id", stored.ID),
		)
		return err
	}
	return nil
}
