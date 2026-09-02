package agentv3

import (
	"csust-got/orm"
	"fmt"
	"sort"
	"time"

	tb "gopkg.in/telebot.v3"
)

const contextStreamMaxLength int64 = 1000

type contextQuery struct {
	RecentSeconds   *int64
	AfterMessageID  *int
	BeforeMessageID *int
	Limit           int
	LimitFrom       string
}

func getFilteredMessageContext(bot *tb.Bot, currentMessage *tb.Message, query contextQuery) ([]*ContextMessage, error) {
	if query.AfterMessageID != nil && query.BeforeMessageID != nil && *query.AfterMessageID >= *query.BeforeMessageID {
		return nil, nil
	}
	if query.AfterMessageID != nil && *query.AfterMessageID >= currentMessage.ID {
		return nil, nil
	}

	beginID := "-"
	if query.AfterMessageID != nil {
		beginID = fmt.Sprintf("(%d", *query.AfterMessageID)
	}

	upperBound := currentMessage.ID
	if query.BeforeMessageID != nil && *query.BeforeMessageID < upperBound {
		upperBound = *query.BeforeMessageID
	}
	endID := fmt.Sprintf("(%d", upperBound)
	storedMessages, err := orm.GetMessagesFromStream(currentMessage.Chat.ID, beginID, endID, contextStreamMaxLength, false)
	if err != nil {
		return nil, err
	}

	messagesByID := make(map[int]*ContextMessage, len(storedMessages))
	for _, msg := range storedMessages {
		if contextMsg := contextMessageFromTelegram(msg); contextMsg != nil {
			messagesByID[contextMsg.ID] = contextMsg
		}
	}

	if currentMessage.ReplyTo != nil {
		liveReplyChain, err := getReplyChain(bot, currentMessage.ReplyTo, int(contextStreamMaxLength)+1)
		if err == nil {
			for _, msg := range liveReplyChain {
				messagesByID[msg.ID] = msg
			}
		}
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

	if len(messages) <= query.Limit {
		return messages, nil
	}
	if query.LimitFrom == "earliest" {
		return messages[:query.Limit], nil
	}
	return messages[len(messages)-query.Limit:], nil
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
