package chat_v2

import (
	"csust-got/log"
	"csust-got/orm"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"

	"github.com/samber/lo"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// ContextMessage 用于存储格式化后的上下文消息
type ContextMessage struct {
	ID        int // 消息ID
	ReplyTo   *int
	User      string
	UserNames userNames
	Text      string
}

type userNames struct {
	First string
	Last  string
}

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
		currentMsgText := currentMsg.Text
		if currentMsgText == "" {
			currentMsgText = currentMsg.Caption
		}
		if currentMsgText != "" {
			var replyID *int
			if currentMsg.ReplyTo != nil {
				replyID = &currentMsg.ReplyTo.ID
			}

			contextMsg := &ContextMessage{
				Text:    currentMsgText,
				ID:      currentMsg.ID,
				ReplyTo: replyID,
				User:    currentMsg.Sender.Username,
				UserNames: userNames{
					First: currentMsg.Sender.FirstName,
					Last:  currentMsg.Sender.LastName,
				},
			}
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
	messages = lo.Map(msgs, func(msg *tb.Message, _ int) *ContextMessage {
		var replyId *int
		if msg.ReplyTo != nil {
			replyId = &msg.ReplyTo.ID
		}

		return &ContextMessage{
			Text:    msg.Text,
			ID:      msg.ID,
			ReplyTo: replyId,
			User:    msg.Sender.Username,
			UserNames: userNames{
				First: msg.Sender.FirstName,
				Last:  msg.Sender.LastName,
			},
		}
	})

	return messages, nil
}

// FormatContextMessages 将上下文消息格式化为字符串
func FormatContextMessages(messages []*ContextMessage) string {
	if len(messages) == 0 {
		return ""
	}

	var result strings.Builder

	for i, msg := range messages {
		// 添加序号而不是用户名
		result.WriteString("[消息 ")
		result.WriteString(strconv.Itoa(msg.ID))
		if msg.User != "" {
			result.WriteString(" from ")
			result.WriteString(msg.User)
			result.WriteString("(")
			result.WriteString(msg.UserNames.ShowName())
			result.WriteString(")")
		}
		if msg.ReplyTo != nil {
			result.WriteString(" reply to ")
			result.WriteString(strconv.Itoa(*msg.ReplyTo))
		}
		result.WriteString("]: ")
		result.WriteString(msg.Text)

		if i < len(messages)-1 {
			result.WriteString("\n\n")
		}
	}

	return result.String()
}

// FormatContextMessagesWithXml 将上下文消息格式化为XML
//
// ```xml
// <messages>
//
//	<message id="1" user="user1"> msg1 escaped</message>
//	<message id="2" user="user2" replyTo="1"> msg2 escaped</message>
//
// </messages>
// ```
func FormatContextMessagesWithXml(messages []*ContextMessage) string {
	buf := strings.Builder{}

	buf.WriteString("<messages>\n")

	for _, msg := range messages {
		buf.WriteString(fmt.Sprintf(`<message id="%d" username="%s" showname="%s"`, msg.ID,
			html.EscapeString(msg.User), html.EscapeString(msg.UserNames.ShowName())))
		if msg.ReplyTo != nil {
			buf.WriteString(fmt.Sprintf(" replyTo=\"%d\"", msg.ReplyTo))
		}
		buf.WriteString(">\n")
		// 将消息文本转义
		buf.WriteString(html.EscapeString(msg.Text))
		buf.WriteString("\n</message>\n")
	}

	buf.WriteString("</messages>\n")

	return buf.String()
}

// FormatSingleTgMessage format tb msg to xml with custom tag
func FormatSingleTbMessage(msg *tb.Message, tag string) string {
	if msg == nil {
		return ""
	}

	buf := strings.Builder{}

	buf.WriteString(fmt.Sprintf(`<%s id="%d" username="%s" showname="%s">`, tag, msg.ID,
		html.EscapeString(msg.Sender.Username),
		html.EscapeString((&userNames{First: msg.Sender.FirstName, Last: msg.Sender.LastName}).ShowName())))

	text := msg.Text
	if text == "" {
		if msg.Photo != nil {
			buf.WriteString("<image_placeholder />")
			text = msg.Photo.Caption
		} else if msg.Document != nil {
			buf.WriteString("<file_placeholder filename=\"")
			buf.WriteString(html.EscapeString(msg.Document.FileName))
			buf.WriteString("\" />")
			text = msg.Document.Caption
		} else if msg.Sticker != nil {
			buf.WriteString("<sticker emoji=\"")
			buf.WriteString(msg.Sticker.Emoji)
			buf.WriteString("\" />")
		}
	}
	buf.WriteString(html.EscapeString(msg.Text))

	buf.WriteString("</")
	buf.WriteString(tag)
	buf.WriteByte('>')

	return buf.String()
}
