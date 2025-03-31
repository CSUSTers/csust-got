package orm

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
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

// PushMessageToStream 将完整的消息结构体保存到 Redis
func PushMessageToStream(msg *Message) error {
	if msg == nil {
		return ErrMessageIsNil
	}

	key := wrapKeyWithChat("message_stream", msg.Chat.ID)

	// 序列化消息对象为JSON
	jsonData, err := json.Marshal(msg)
	if err != nil {
		log.Error("marshal message to json failed", zap.Int64("chat", msg.Chat.ID), zap.Int("message", msg.ID), zap.Error(err))
		return err
	}

	resp := rc.XAdd(context.TODO(), &redis.XAddArgs{
		Stream: key,
		MaxLen: 1000,
		Approx: true,
		ID:     strconv.Itoa(msg.ID),
		Values: []any{"message", jsonData},
	})
	if resp.Err() != nil {
		log.Error("push message to redis stream failed", zap.Int64("chat", msg.Chat.ID), zap.Int("message", msg.ID), zap.Error(err))
		return err
	}
	_ = rc.Expire(context.TODO(), key, 24*time.Hour)
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

// GetMessagesFromStream 从 Redis 获取消息
func GetMessagesFromStream(chatID int64, beginID, endID string, count int64, reverse bool) ([]*Message, error) {
	key := wrapKeyWithChat("message_stream", chatID)

	var resp *redis.XMessageSliceCmd
	if reverse {
		resp = rc.XRevRangeN(context.TODO(), key, beginID, endID, count)
	} else {
		resp = rc.XRangeN(context.TODO(), key, beginID, endID, count)
	}
	if resp.Err() != nil {
		log.Error("get messages from redis stream failed", zap.Int64("chat", chatID),
			zap.String("begin", beginID), zap.String("end", endID), zap.Error(resp.Err()))
		return nil, resp.Err()
	}

	messages := make([]*Message, len(resp.Val()))

	for _, msg := range resp.Val() {
		var message Message
		err := json.Unmarshal(msg.Values["message"].([]byte), &message)
		if err != nil {
			log.Error("unmarshal message failed", zap.Int64("chat", chatID), zap.Any("message", msg), zap.Error(err))
			return nil, err
		}
		messages = append(messages, &message)
	}

	return messages, nil
}
