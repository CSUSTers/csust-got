package agentv3

import (
	"csust-got/log"
	"csust-got/orm"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

const (
	contextMessageTypeText     = "text"
	contextMessageTypePhoto    = "photo"
	contextMessageTypeSticker  = "sticker"
	contextMessageTypeDocument = "document"
)

// ContextMessage 用于存储格式化后的上下文消息
type ContextMessage struct {
	ID          int // 消息ID
	ReplyTo     *int
	User        string
	UserNames   userNames
	Text        string
	Type        string
	PhotoFileID string
}

// userNames represents a user's first and last name
type userNames struct {
	First string
	Last  string
}

// ShowName returns the formatted display name
func (u *userNames) ShowName() string {
	bs := strings.Builder{}

	if u.First != "" {
		bs.WriteString(u.First)
	}

	if u.Last != "" {
		if u.First != "" {
			bs.WriteString(" ")
		}
		bs.WriteString(u.Last)
	}

	return bs.String()
}

func (u *userNames) String() string {
	return u.ShowName()
}

func contextMessageFromTelegram(msg *tb.Message) *ContextMessage {
	if msg == nil {
		return nil
	}

	contextMsg := &ContextMessage{
		ID:   msg.ID,
		Text: getMessageTextWithEntities(msg, false),
		Type: contextMessageTypeText,
	}
	if msg.ReplyTo != nil {
		contextMsg.ReplyTo = &msg.ReplyTo.ID
	}
	if msg.Sender != nil {
		contextMsg.User = msg.Sender.Username
		contextMsg.UserNames = userNames{
			First: msg.Sender.FirstName,
			Last:  msg.Sender.LastName,
		}
	}

	switch {
	case msg.Photo != nil:
		contextMsg.Type = contextMessageTypePhoto
		contextMsg.PhotoFileID = msg.Photo.FileID
	case msg.Sticker != nil:
		contextMsg.Type = contextMessageTypeSticker
		if contextMsg.Text == "" {
			contextMsg.Text = msg.Sticker.Emoji
		}
	case msg.Document != nil:
		contextMsg.Type = contextMessageTypeDocument
		if contextMsg.Text == "" {
			contextMsg.Text = msg.Document.FileName
		}
	}

	if contextMsg.Text == "" && contextMsg.Type == contextMessageTypeText {
		return nil
	}
	return contextMsg
}

// GetMessageContext 获取消息的上下文
// 返回的消息数组按照时间顺序排列，最早的消息在前，最新的消息在后
func GetMessageContext(bot *tb.Bot, msg *tb.Message, maxContext int) ([]*ContextMessage, error) {
	var messages []*ContextMessage
	var result []*ContextMessage

	// 如果存在回复链，收集回复链上的消息
	if msg.ReplyTo != nil {
		replyChain, err := getReplyChain(bot, msg.ReplyTo, maxContext)
		if err != nil {
			log.Error("[MessageContext] Failed to get reply chain", zap.Error(err))
			// 继续执行，只是回复链获取失败而已
		} else {
			messages = append(replyChain, messages...)
		}
	}

	// 如果消息数量不足maxContext，通过消息ID向前查找
	curMsgID := msg.ID
	if len(messages) > 0 {
		curMsgID = messages[0].ID
	}
	if len(messages) < maxContext {
		additionalMessages, err := getPreviousMessages(msg.Chat.ID, curMsgID, maxContext-len(messages))
		if err != nil {
			log.Error("[MessageContext] Failed to get previous messages", zap.Error(err))
		} else {
			messages = append(additionalMessages, messages...)
		}
	}

	// 取最多maxContext条消息
	if len(messages) > maxContext {
		result = messages[len(messages)-maxContext:]
	} else {
		result = messages
	}

	return result, nil
}

// getReplyChain 获取回复链上的所有消息，按照时间顺序排列（最早的消息在前）
func getReplyChain(bot *tb.Bot, msg *tb.Message, maxContext int) ([]*ContextMessage, error) {
	var chain []*ContextMessage
	currentMsg := msg
	visited := make(map[int]bool) // 避免出现回复循环

	// 向上追溯回复链
	for currentMsg != nil && len(chain) < maxContext-1 {
		if visited[currentMsg.ID] {
			// 检测到循环引用，跳出循环
			break
		}

		visited[currentMsg.ID] = true
		if contextMsg := contextMessageFromTelegram(currentMsg); contextMsg != nil {
			// 将消息添加到链的前面，这样链就是按时间顺序排列的
			chain = append(chain, contextMsg)
		}

		// 继续向上追溯
		if currentMsg.ReplyTo == nil {
			break
		}
		currentMsg = currentMsg.ReplyTo
	}
	slices.Reverse(chain)

	return chain, nil
}

// getPreviousMessages 通过消息ID获取之前的消息
func getPreviousMessages(chatID int64, messageID int, count int) ([]*ContextMessage, error) {
	var messages []*ContextMessage

	msgs, err := orm.GetMessagesFromStream(chatID, fmt.Sprintf("(%d", messageID), strconv.Itoa(messageID-50), int64(count), true)

	if err != nil {
		return messages, err
	}
	slices.Reverse(msgs)
	for _, msg := range msgs {
		if contextMsg := contextMessageFromTelegram(msg); contextMsg != nil {
			messages = append(messages, contextMsg)
		}
	}

	return messages, nil
}
