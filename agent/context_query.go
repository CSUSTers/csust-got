package agentv3

import (
	"context"
	"sort"
	"time"

	"csust-got/orm"

	tb "gopkg.in/telebot.v3"
)

const (
	contextStreamMaxLength int64 = 1000
	contextLimitLatest           = "latest"
	contextLimitEarliest         = "earliest"
	contextToolName              = "get_context"
)

type contextQuery struct {
	RecentSeconds   *int64
	AfterMessageID  *int
	BeforeMessageID *int
	Limit           int
	LimitFrom       string
}

type contextQueryResult struct {
	Messages       []*ContextMessage
	Selection      string
	CandidateScope string
}

func getFilteredMessageContext(ctx context.Context, bot *tb.Bot, currentMessage *tb.Message, query contextQuery) (contextQueryResult, error) {
	result := contextQueryResult{Selection: query.LimitFrom, CandidateScope: "filtered_cache_and_reply_chain"}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	ctx, done := withContextMessageResolver(ctx)
	defer done()
	resolver := ctx.Value(contextMessageResolverKey{}).(*contextMessageResolver)
	if query.AfterMessageID != nil && query.BeforeMessageID != nil && *query.AfterMessageID >= *query.BeforeMessageID {
		return result, nil
	}
	if query.AfterMessageID != nil && *query.AfterMessageID >= currentMessage.ID {
		return result, nil
	}

	streamQuery := orm.MessageStreamQuery{MaxID: currentMessage.ID - 1, Count: contextStreamMaxLength}
	if query.AfterMessageID != nil {
		streamQuery.MinID = *query.AfterMessageID + 1
	}
	if query.BeforeMessageID != nil && *query.BeforeMessageID <= currentMessage.ID {
		streamQuery.MaxID = *query.BeforeMessageID - 1
	}
	if streamQuery.MaxID <= 0 {
		return result, nil
	}
	storedMessages, err := resolver.messageReader().GetMessagesFromStream(currentMessage.Chat.ID, streamQuery)
	if err != nil {
		return result, err
	}
	if err := resolver.prime(currentMessage.Chat.ID, append(storedMessages, currentMessage)...); err != nil {
		return result, err
	}

	messagesByID := make(map[int]*ContextMessage, len(storedMessages))
	for _, msg := range storedMessages {
		resolved, process, err := resolveContextMessage(ctx, currentMessage.Chat.ID, msg)
		if err != nil {
			return result, err
		}
		if contextMsg := contextMessageFromTelegram(resolved); !process && contextMsg != nil {
			messagesByID[contextMsg.ID] = contextMsg
		}
	}

	if currentMessage.ReplyTo != nil {
		parent, _ := replySessionEmbeddedParent(currentMessage.ReplyTo, currentMessage.Chat)
		liveReplyChain, err := getReplyChainContext(ctx, bot, parent, int(contextStreamMaxLength)+1)
		if err == nil {
			for _, msg := range liveReplyChain {
				messagesByID[msg.ID] = msg
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	now := time.Now()
	messages := make([]*ContextMessage, 0, len(messagesByID))
	for _, msg := range messagesByID {
		if matchesContextQuery(msg, currentMessage.ID, query, now) {
			messages = append(messages, msg)
		}
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].ID < messages[j].ID
	})

	result.Messages = messages
	return result, nil
}

func matchesContextQuery(msg *ContextMessage, currentMessageID int, query contextQuery, now time.Time) bool {
	if msg.ID >= currentMessageID {
		return false
	}
	if query.AfterMessageID != nil && msg.ID <= *query.AfterMessageID {
		return false
	}
	if query.BeforeMessageID != nil && msg.ID >= *query.BeforeMessageID {
		return false
	}
	if query.RecentSeconds != nil && *query.RecentSeconds > 0 {
		cutoff := now.Unix() - *query.RecentSeconds
		if msg.Unixtime < cutoff {
			return false
		}
	}
	return true
}
