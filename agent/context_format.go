package agentv3

import (
	"csust-got/util"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode"

	tb "gopkg.in/telebot.v3"
)

// FormatContextMessages 将上下文消息格式化为字符串
func FormatContextMessages(messages []*ContextMessage) string {
	if len(messages) == 0 {
		return ""
	}

	var result strings.Builder
	now := time.Now()

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
		if messageTime := formatContextMessageTime(msg.Unixtime, now); messageTime != "" {
			result.WriteString(" time ")
			result.WriteString(messageTime)
		}
		result.WriteString("]: ")
		result.WriteString(msg.Text)

		if i < len(messages)-1 {
			result.WriteString("\n\n")
		}
	}

	return result.String()
}

func formatContextMessageTime(unixtime int64, _ time.Time) string {
	if unixtime == 0 {
		return ""
	}

	return time.Unix(unixtime, 0).In(util.TimeZoneCST).Format(time.RFC3339)
}

// FormatContextMessagesWithXml 将上下文消息格式化为嵌套XML格式
// 消息回复链被表示为嵌套结构，回复消息嵌入到被回复的消息中
func FormatContextMessagesWithXml(messages []*ContextMessage) string {
	return formatContextMessagesWithMetadata(messages, "")
}

func formatContextMessagesWithMetadata(messages []*ContextMessage, metadata string) string {
	if len(messages) == 0 && metadata == "" {
		return ""
	}

	// 创建消息映射，方便查找
	msgMap := make(map[int]*ContextMessage)
	for _, msg := range messages {
		msgMap[msg.ID] = msg
	}

	// 找到根消息（没有被回复的消息）
	rootMessages := make([]*ContextMessage, 0)
	for _, msg := range messages {
		if msg.ReplyTo == nil || msgMap[*msg.ReplyTo] == nil {
			rootMessages = append(rootMessages, msg)
		}
	}

	// 为每个消息找到它的直接回复消息
	replies := make(map[int][]*ContextMessage)
	for _, msg := range messages {
		if msg.ReplyTo != nil {
			replyToID := *msg.ReplyTo
			replies[replyToID] = append(replies[replyToID], msg)
		}
	}

	buf := strings.Builder{}
	buf.WriteString("<messages>\n")
	buf.WriteString(metadata)
	now := time.Now()
	visited := make(map[int]bool, len(messages))

	// 递归渲染每个根消息及其回复链
	for _, rootMsg := range rootMessages {
		renderNestedMessage(&buf, rootMsg, replies, 0, now, visited)
	}
	for _, msg := range messages {
		if !visited[msg.ID] {
			renderNestedMessage(&buf, msg, replies, 0, now, visited)
		}
	}

	buf.WriteString("</messages>\n")
	return buf.String()
}

// renderNestedMessage 递归渲染嵌套消息
func renderNestedMessage(buf *strings.Builder, msg *ContextMessage, replies map[int][]*ContextMessage, depth int, now time.Time, visited map[int]bool) {
	if visited[msg.ID] {
		return
	}
	visited[msg.ID] = true
	indent := strings.Repeat("  ", depth+1)
	messageType := msg.Type
	if messageType == "" {
		messageType = contextMessageTypeText
	}

	// 开始标签
	buf.WriteString(indent)
	fmt.Fprintf(buf, `<message id="%d" username="%s" showname="%s" type="%s"`,
		msg.ID, escapeContextXML(msg.User), escapeContextXML(msg.UserNames.ShowName()), escapeContextXML(messageType))

	if msg.ReplyTo != nil {
		fmt.Fprintf(buf, ` replyTo="%d"`, *msg.ReplyTo)
	}
	if messageTime := formatContextMessageTime(msg.Unixtime, now); messageTime != "" {
		fmt.Fprintf(buf, ` time="%s"`, html.EscapeString(messageTime))
	}
	buf.WriteString(">\n")

	if messageType == contextMessageTypePhoto {
		buf.WriteString(indent)
		buf.WriteString("  ")
		fmt.Fprintf(buf, `<image file_id="%s" />`, escapeContextXML(msg.PhotoFileID))
		buf.WriteString("\n")
	}
	if msg.Text != "" {
		buf.WriteString(indent)
		buf.WriteString("  ")
		buf.WriteString(escapeContextXML(msg.Text))
		buf.WriteString("\n")
	}

	// 递归渲染回复消息
	if msgReplies, exists := replies[msg.ID]; exists {
		for _, reply := range msgReplies {
			renderNestedMessage(buf, reply, replies, depth+1, now, visited)
		}
	}

	// 结束标签
	buf.WriteString(indent)
	buf.WriteString("</message>\n")
}

func escapeContextXML(text string) string {
	return html.EscapeString(strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r <= 0xd7ff || r >= 0xe000 && r <= 0xfffd || r >= 0x10000 && r <= unicode.MaxRune {
			return r
		}
		return unicode.ReplacementChar
	}, text))
}

// FormatSingleTbMessage format tb msg to xml with custom tag
func FormatSingleTbMessage(msg *tb.Message, tag string) string {
	if msg == nil {
		return ""
	}

	buf := strings.Builder{}
	username, showname := "", ""
	if msg.Sender != nil {
		username = msg.Sender.Username
		showname = (&userNames{First: msg.Sender.FirstName, Last: msg.Sender.LastName}).ShowName()
	}
	fmt.Fprintf(&buf, `<%s id="%d" username="%s" showname="%s">\n`, tag, msg.ID,
		html.EscapeString(username), html.EscapeString(showname))

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
