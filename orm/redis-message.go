package orm

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"csust-got/log"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

// SetMessage 将完整的消息结构体保存到 Redis
func SetMessage(msg *Message) error {
	if msg == nil {
		return ErrMessageIsNil
	}

	key := wrapKeyWithChatMsg("message_full", msg.Chat.ID, msg.ID)

	// 序列化消息对象为JSON
	jsonData, err := json.Marshal(msg)
	if err != nil {
		log.Error("marshal message to json failed", zap.Int64("chat", msg.Chat.ID), zap.Int("message", msg.ID), zap.Error(err))
		return err
	}

	err = rc.Set(context.TODO(), key, jsonData, 24*time.Hour).Err()
	if err != nil {
		log.Error("set message to redis failed", zap.Int64("chat", msg.Chat.ID), zap.Int("message", msg.ID), zap.Error(err))
		return err
	}
	return nil
}

// GetMessage 从 Redis 获取完整的消息结构体
func GetMessage(chatID int64, messageID int) (*Message, error) {
	key := wrapKeyWithChatMsg("message_full", chatID, messageID)

	jsonData, err := rc.Get(context.TODO(), key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("get message from redis failed", zap.Int64("chat", chatID), zap.Int("message", messageID), zap.Error(err))
		}
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(jsonData, &msg); err != nil {
		log.Error("unmarshal message failed", zap.Int64("chat", chatID), zap.Int("message", messageID), zap.Error(err))
		return nil, err
	}

	return &msg, nil
}
