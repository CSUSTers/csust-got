package orm

import (
	"context"
	"errors"
	"time"

	"csust-got/log"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// SetMessageText 将消息文本保存到 Redis
func SetMessageText(chatID int64, messageID int, text string) error {
	key := wrapKeyWithChatMsg("message_text", chatID, messageID)
	err := rc.Set(context.TODO(), key, text, 24*time.Hour).Err()
	if err != nil {
		log.Error("set message text to redis failed", zap.Int64("chat", chatID), zap.Int("message", messageID), zap.Error(err))
		return err
	}
	return nil
}

// GetMessageText 从 Redis 获取消息文本
func GetMessageText(chatID int64, messageID int) (string, error) {
	key := wrapKeyWithChatMsg("message_text", chatID, messageID)
	text, err := rc.Get(context.TODO(), key).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Error("get message text from redis failed", zap.Int64("chat", chatID), zap.Int("message", messageID), zap.Error(err))
		}
		return "", err
	}
	return text, nil
}
