package chat_v2

import (
	"csust-got/log"
	"csust-got/orm"
	"errors"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// ContextMessage 用于存储格式化后的上下文消息
type ContextMessage struct {
	ID   int // 消息ID
	Text string
}

// GetMessageContext 获取消息的上下文
// 返回的消息数组按照时间顺序排列，最早的消息在前，最新的消息在后
func GetMessageContext(bot *tb.Bot, msg *tb.Message, maxContext int) ([]ContextMessage, error) {
	var messages []ContextMessage
	var result []ContextMessage

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
func getReplyChain(bot *tb.Bot, msg *tb.Message, maxContext int) ([]ContextMessage, error) {
	var chain []ContextMessage
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
			contextMsg := ContextMessage{
				Text: currentMsgText,
				ID:   currentMsg.ID,
			}
			// 将消息添加到链的前面，这样链就是按时间顺序排列的
			chain = append([]ContextMessage{contextMsg}, chain...)
		}

		// 继续向上追溯
		if currentMsg.ReplyTo == nil {
			break
		}
		currentMsg = currentMsg.ReplyTo
	}

	return chain, nil
}

// getPreviousMessages 通过消息ID获取之前的消息
func getPreviousMessages(chatID int64, messageID int, count int) ([]ContextMessage, error) {
	var messages []ContextMessage

	for i := 1; i <= 50 && len(messages) < count; i++ { // 最多扫描50条消息
		prevMsgID := messageID - i
		if prevMsgID <= 0 {
			break
		}

		// 获取完整消息
		msg, err := orm.GetMessage(chatID, prevMsgID)
		if err != nil {
			if errors.Is(err, redis.Nil) {
				// 消息不存在或已被删除，继续查找
				continue
			}
			return messages, err
		}

		// 从完整消息中提取文本
		msgText := msg.Text
		if msgText == "" {
			msgText = msg.Caption
		}

		if msgText != "" {
			messages = append([]ContextMessage{{Text: msgText, ID: msg.ID}}, messages...)
		}
	}

	return messages, nil
}

// FormatContextMessages 将上下文消息格式化为字符串
func FormatContextMessages(messages []ContextMessage) string {
	if len(messages) == 0 {
		return ""
	}

	var result strings.Builder

	for i, msg := range messages {
		// 添加序号而不是用户名
		result.WriteString("消息 ")
		result.WriteString(strconv.Itoa(msg.ID))
		result.WriteString(": ")
		result.WriteString(msg.Text)

		if i < len(messages)-1 {
			result.WriteString("\n\n")
		}
	}

	return result.String()
}
