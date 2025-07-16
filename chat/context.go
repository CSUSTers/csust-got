package chat

import (
	"csust-got/log"
	"csust-got/orm"
	"fmt"
	"html"
	"slices"
	"sort"
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

// getMessageTextWithEntities reconstructs the formatted text from a Telegram message
// using its entities to preserve links and other formatting that would be lost in raw Text field.
// 
// This function solves the issue where chat AI models couldn't access URLs from formatted links.
// When users send messages like [title](url) or <a href="url">title</a>, Telegram stores:
// - Text field: only the visible text ("title")  
// - Entities field: formatting info including the actual URL
//
// This function reconstructs the original formatted text by combining both fields.
// It returns markdown-formatted text by default, or HTML if htmlFormat is true.
func getMessageTextWithEntities(msg *tb.Message, htmlFormat bool) string {
	if msg == nil {
		return ""
	}

	// Get the raw text - prefer Text over Caption
	text := msg.Text
	entities := msg.Entities
	if text == "" {
		text = msg.Caption
		entities = msg.CaptionEntities
	}

	// If no entities, return the raw text
	if len(entities) == 0 {
		return text
	}

	// Sort entities by offset to process them in order
	sortedEntities := make([]tb.MessageEntity, len(entities))
	copy(sortedEntities, entities)
	sort.Slice(sortedEntities, func(i, j int) bool {
		return sortedEntities[i].Offset < sortedEntities[j].Offset
	})

	// Convert text to runes for proper UTF-16 handling
	runes := []rune(text)
	var result strings.Builder
	lastOffset := 0

	for _, entity := range sortedEntities {
		// Add text before this entity
		if entity.Offset > lastOffset {
			result.WriteString(string(runes[lastOffset:entity.Offset]))
		}

		// Get the entity text using the built-in method
		entityText := msg.EntityText(entity)
		
		// Format the entity based on its type
		switch entity.Type {
		case tb.EntityTextLink:
			// This is a formatted link like [text](url)
			if htmlFormat {
				result.WriteString(fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(entity.URL), html.EscapeString(entityText)))
			} else {
				result.WriteString(fmt.Sprintf("[%s](%s)", entityText, entity.URL))
			}
		case tb.EntityURL:
			// This is a bare URL
			result.WriteString(entityText)
		case tb.EntityBold:
			if htmlFormat {
				result.WriteString(fmt.Sprintf("<b>%s</b>", html.EscapeString(entityText)))
			} else {
				result.WriteString(fmt.Sprintf("**%s**", entityText))
			}
		case tb.EntityItalic:
			if htmlFormat {
				result.WriteString(fmt.Sprintf("<i>%s</i>", html.EscapeString(entityText)))
			} else {
				result.WriteString(fmt.Sprintf("*%s*", entityText))
			}
		case tb.EntityCode:
			if htmlFormat {
				result.WriteString(fmt.Sprintf("<code>%s</code>", html.EscapeString(entityText)))
			} else {
				result.WriteString(fmt.Sprintf("`%s`", entityText))
			}
		case tb.EntityMention:
			// Convert mentions to [@username](tg:username) format
			if htmlFormat {
				// For HTML format, remove @ and use tg:// scheme
				username := strings.TrimPrefix(entityText, "@")
				result.WriteString(fmt.Sprintf(`<a href="tg:%s">%s</a>`, username, entityText))
			} else {
				// For markdown format, use [@username](tg:username) format
				username := strings.TrimPrefix(entityText, "@")
				result.WriteString(fmt.Sprintf("[%s](tg:%s)", entityText, username))
			}
		case tb.EntityHashtag, tb.EntityEmail:
			// These entities are already properly formatted in the text
			result.WriteString(entityText)
		default:
			// For unknown entities, just add the text as-is
			result.WriteString(entityText)
		}

		lastOffset = entity.Offset + entity.Length
	}

	// Add remaining text after the last entity
	if lastOffset < len(runes) {
		result.WriteString(string(runes[lastOffset:]))
	}

	return result.String()
}

// getTextSubstring safely extracts a substring using UTF-16 offsets (like Telegram entities)
// This is needed because Telegram entities use UTF-16 code unit offsets, not byte offsets
func getTextSubstring(text string, start, end int) string {
	runes := []rune(text)
	if start < 0 {
		start = 0
	}
	if end > len(runes) {
		end = len(runes)
	}
	if start >= end {
		return ""
	}
	return string(runes[start:end])
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
		currentMsgText := getMessageTextWithEntities(currentMsg, false) // Use markdown format
		if currentMsgText == "" {
			// Fallback to raw text if no entities
			currentMsgText = currentMsg.Text
			if currentMsgText == "" {
				currentMsgText = currentMsg.Caption
			}
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

		// Use the helper to get formatted text with entities
		msgText := getMessageTextWithEntities(msg, false) // Use markdown format
		if msgText == "" {
			// Fallback to raw text if no entities
			msgText = msg.Text
		}

		return &ContextMessage{
			Text:    msgText,
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

// FormatSingleTbMessage format tb msg to xml with custom tag
func FormatSingleTbMessage(msg *tb.Message, tag string) string {
	if msg == nil {
		return ""
	}

	buf := strings.Builder{}

	buf.WriteString(fmt.Sprintf(`<%s id="%d" username="%s" showname="%s">\n`, tag, msg.ID,
		html.EscapeString(msg.Sender.Username),
		html.EscapeString((&userNames{First: msg.Sender.FirstName, Last: msg.Sender.LastName}).ShowName())))

	text := getMessageTextWithEntities(msg, true) // Use HTML format since this function generates XML/HTML
	if text == "" {
		// Fallback to raw text
		text = msg.Text
		if text == "" {
			text = msg.Caption
		}
	}
	if text == "" {
		switch {
		case msg.Photo != nil:
			buf.WriteString("<image_placeholder />\n")
			text = msg.Photo.Caption
		case msg.Document != nil:
			buf.WriteString("<file_placeholder filename=\"")
			buf.WriteString(html.EscapeString(msg.Document.FileName))
			buf.WriteString("\" />\n")
			text = msg.Document.Caption
		case msg.Sticker != nil:
			buf.WriteString("<sticker emoji=\"")
			buf.WriteString(msg.Sticker.Emoji)
			buf.WriteString("\" />\n")
		}
	}
	buf.WriteString(html.EscapeString(text))

	buf.WriteString("\n</")
	buf.WriteString(tag)
	buf.WriteByte('>')

	return buf.String()
}
