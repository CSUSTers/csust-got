package orm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"csust-got/log"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

// ErrInvalidCachedMessage distinguishes recoverable cache corruption from Redis failures.
var ErrInvalidCachedMessage = errors.New("invalid cached message")

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
		log.Error("push message to redis stream failed", zap.Int64("chat", msg.Chat.ID), zap.Int("message", msg.ID), zap.Error(resp.Err()))
		return resp.Err()
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

	msg, err := decodeCachedMessage(jsonData)
	if err != nil {
		log.Error("decode cached message failed", zap.Int64("chat", chatID), zap.Int("message", messageID), zap.Error(err))
		return nil, err
	}

	return msg, nil
}

// GetMessagesFromStream 从 Redis 获取消息
func GetMessagesFromStream(chatID int64, beginID, endID string, count int64, reverse bool) ([]*Message, error) {
	messages, _, err := getMessagesFromStream(chatID, beginID, endID, count, reverse, false)
	return messages, err
}

// GetMessagesFromStreamBestEffort skips malformed stream records and returns the number of records scanned.
func GetMessagesFromStreamBestEffort(chatID int64, beginID, endID string, count int64, reverse bool) ([]*Message, int, error) {
	return getMessagesFromStream(chatID, beginID, endID, count, reverse, true)
}

func getMessagesFromStream(chatID int64, beginID, endID string, count int64, reverse, bestEffort bool) ([]*Message, int, error) {
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
		return nil, 0, resp.Err()
	}

	records := resp.Val()
	messages := make([]*Message, 0, len(records))

	for _, record := range records {
		encoded, ok := record.Values["message"].(string)
		if !ok {
			err := fmt.Errorf("%w: invalid_stream_message_field", ErrInvalidCachedMessage)
			log.Error("decode cached stream message failed", zap.Int64("chat", chatID), zap.String("stream", record.ID),
				zap.String("field", "message"), zap.Error(err))
			if bestEffort {
				continue
			}
			return nil, len(records), err
		}

		message, err := decodeCachedMessage([]byte(encoded))
		if err != nil {
			log.Error("decode cached stream message failed", zap.Int64("chat", chatID), zap.String("stream", record.ID),
				zap.String("field", "message"), zap.Error(err))
			if bestEffort {
				continue
			}
			return nil, len(records), err
		}
		messages = append(messages, message)
	}

	return messages, len(records), nil
}

func decodeCachedMessage(data []byte) (*Message, error) {
	// Telebot marshals PollType as an object but only decodes its string form.
	// Keep integer precision while normalizing nested cached polls.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: invalid_json", ErrInvalidCachedMessage)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: invalid_json", ErrInvalidCachedMessage)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("%w: invalid_message_shape", ErrInvalidCachedMessage)
	}
	if err := normalizeCachedPollTypes(value); err != nil {
		return nil, err
	}

	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid_message_shape", ErrInvalidCachedMessage)
	}
	var message Message
	if err := json.Unmarshal(normalized, &message); err != nil {
		return nil, fmt.Errorf("%w: invalid_message_schema", ErrInvalidCachedMessage)
	}
	return &message, nil
}

func normalizeCachedPollTypes(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		if rawPoll, exists := typed["poll"]; exists && rawPoll != nil {
			poll, ok := rawPoll.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: invalid_poll", ErrInvalidCachedMessage)
			}
			if err := normalizeCachedPollType(poll); err != nil {
				return err
			}
		}
		for _, nested := range typed {
			if err := normalizeCachedPollTypes(nested); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range typed {
			if err := normalizeCachedPollTypes(nested); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeCachedPollType(poll map[string]any) error {
	rawType, exists := poll["type"]
	if !exists {
		return fmt.Errorf("%w: invalid_poll_type", ErrInvalidCachedMessage)
	}

	var pollType string
	switch typed := rawType.(type) {
	case string:
		pollType = typed
	case map[string]any:
		if len(typed) != 1 {
			return fmt.Errorf("%w: invalid_poll_type", ErrInvalidCachedMessage)
		}
		var ok bool
		pollType, ok = typed["type"].(string)
		if !ok {
			return fmt.Errorf("%w: invalid_poll_type", ErrInvalidCachedMessage)
		}
	default:
		return fmt.Errorf("%w: invalid_poll_type", ErrInvalidCachedMessage)
	}

	if pollType != string(PollRegular) && pollType != string(PollQuiz) {
		return fmt.Errorf("%w: invalid_poll_type", ErrInvalidCachedMessage)
	}
	poll["type"] = pollType
	return nil
}
