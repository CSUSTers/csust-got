package orm

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"csust-got/log"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	. "gopkg.in/telebot.v3"
)

// ErrInvalidCachedMessage distinguishes recoverable cache corruption from Redis failures.
var ErrInvalidCachedMessage = errors.New("invalid cached message")

// SetMessage stores the full snapshot unless the cache already holds a newer edit of the same message.
func SetMessage(msg *Message) error {
	if msg == nil {
		return ErrMessageIsNil
	}

	key := wrapKeyWithChatMsg("message_full", msg.Chat.ID, msg.ID)

	jsonData, err := json.Marshal(msg)
	if err != nil {
		log.Error("marshal message to json failed", zap.Int64("chat", msg.Chat.ID), zap.Int("message", msg.ID), zap.Error(err))
		return err
	}

	if err = setMessageIfNewer(context.TODO(), key, msg, jsonData); err != nil {
		log.Error("set message to redis failed", zap.Int64("chat", msg.Chat.ID), zap.Int("message", msg.ID), zap.Error(err))
		return err
	}
	return nil
}

// setMessageScript replaces the snapshot only when the incoming edit_date/date are at least as new as the stored ones.
var setMessageScript = redis.NewScript(`
local stored = redis.call('GET', KEYS[1])
if stored then
  local ok, current = pcall(cjson.decode, stored)
  if ok and type(current) == 'table' then
    local edit = tonumber(current['edit_date']) or 0
    local date = tonumber(current['date']) or 0
    local newEdit = tonumber(ARGV[2])
    local newDate = tonumber(ARGV[3])
    if newEdit < edit or (newEdit == edit and newDate < date) then
      return 0
    end
  end
end
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[4])
return 1
`)

func setMessageIfNewer(ctx context.Context, key string, msg *Message, jsonData []byte) error {
	ttl := int64((24 * time.Hour).Seconds())
	return setMessageScript.Run(ctx, rc, []string{key}, jsonData, msg.LastEdit, msg.Unixtime, ttl).Err()
}

// compareMessageSnapshots orders snapshots of one message by edit time, then send time.
func compareMessageSnapshots(a, b *Message) int {
	if c := cmp.Compare(a.LastEdit, b.LastEdit); c != 0 {
		return c
	}
	return cmp.Compare(a.Unixtime, b.Unixtime)
}

const (
	messageStreamMaxLen       int64 = 1000
	messageStreamFieldMessage       = "message"
	messageStreamFieldID            = "id"
	messageStreamFieldEdited        = "edited"
)

// MessageStreamQuery selects cached messages by Telegram message ID; zero bounds are open.
type MessageStreamQuery struct {
	MinID   int
	MaxID   int
	Count   int64
	Reverse bool
}

// PushMessageToStream appends a message snapshot; entry IDs are server-generated so late or edited pushes never fail.
func PushMessageToStream(msg *Message) error {
	if msg == nil {
		return ErrMessageIsNil
	}

	key := wrapKeyWithChat("message_stream", msg.Chat.ID)

	jsonData, err := json.Marshal(msg)
	if err != nil {
		log.Error("marshal message to json failed", zap.Int64("chat", msg.Chat.ID), zap.Int("message", msg.ID), zap.Error(err))
		return err
	}

	values := []any{messageStreamFieldID, strconv.Itoa(msg.ID), messageStreamFieldMessage, jsonData}
	if msg.LastEdit != 0 {
		values = append(values, messageStreamFieldEdited, "1")
	}
	resp := rc.XAdd(context.TODO(), &redis.XAddArgs{
		Stream: key,
		MaxLen: messageStreamMaxLen,
		Approx: true,
		ID:     "*",
		Values: values,
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
	return getMessageContext(context.Background(), rc, chatID, messageID)
}

// GetMessageContext isolates the connection so cancellation can interrupt socket reads.
func GetMessageContext(ctx context.Context, chatID int64, messageID int) (*Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ctx.Done() == nil {
		return getMessageContext(ctx, rc, chatID, messageID)
	}
	reader := NewMessageReader(ctx)
	defer reader.Close()
	return getMessageContext(ctx, reader.client, chatID, messageID)
}

// MessageReader is an operation-scoped, cancellable batch reader.
type MessageReader struct {
	ctx       context.Context
	client    *redis.Client
	stop      func() bool
	closed    chan struct{}
	closeOnce sync.Once
}

// NewMessageReader owns one cancellable client for an entire message query operation.
func NewMessageReader(ctx context.Context) *MessageReader {
	options := *rc.Options()
	options.ContextTimeoutEnabled = true
	options.PushNotificationProcessor = nil
	options.PoolSize = 1
	options.MinIdleConns = 0
	reader := &MessageReader{ctx: ctx, client: redis.NewClient(&options), closed: make(chan struct{})}
	reader.stop = context.AfterFunc(ctx, func() {
		_ = reader.client.Close()
		close(reader.closed)
	})
	return reader
}

// Close releases the private client and joins any in-flight cancellation cleanup.
func (r *MessageReader) Close() {
	r.closeOnce.Do(func() {
		if !r.stop() {
			<-r.closed
		} else {
			_ = r.client.Close()
			close(r.closed)
		}
	})
}

// GetMessages skips missing or malformed records; Redis failures remain operation errors.
func (r *MessageReader) GetMessages(chatID int64, messageIDs []int) (map[int]*Message, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	messages := make(map[int]*Message, len(messageIDs))
	if len(messageIDs) == 0 {
		return messages, nil
	}
	keys := make([]string, len(messageIDs))
	for i, id := range messageIDs {
		keys[i] = wrapKeyWithChatMsg("message_full", chatID, id)
	}
	values, err := r.client.MGet(r.ctx, keys...).Result()
	if r.ctx.Err() != nil {
		return nil, r.ctx.Err()
	}
	if err != nil {
		log.Error("get messages from redis failed", zap.Int64("chat", chatID), zap.Error(err))
		return nil, err
	}
	for i, value := range values {
		if err := r.ctx.Err(); err != nil {
			return nil, err
		}
		if value == nil {
			continue
		}
		data, ok := value.(string)
		if !ok {
			continue
		}
		message, err := decodeCachedMessage([]byte(data))
		if err != nil {
			log.Error("decode cached message failed", zap.Int64("chat", chatID), zap.Int("message", messageIDs[i]), zap.Error(err))
			continue
		}
		messages[messageIDs[i]] = message
	}
	return messages, nil
}

func getMessageContext(ctx context.Context, client *redis.Client, chatID int64, messageID int) (*Message, error) {
	key := wrapKeyWithChatMsg("message_full", chatID, messageID)

	jsonData, err := client.Get(ctx, key).Bytes()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
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

// GetMessagesFromStream returns distinct messages in the query range sorted by message ID, keeping the newest snapshot per ID by edit time, send time, then stream order.
func GetMessagesFromStream(chatID int64, query MessageStreamQuery) ([]*Message, error) {
	messages, _, err := getMessagesFromStream(chatID, query, false)
	return messages, err
}

// GetMessagesFromStreamBestEffort skips malformed stream records and returns the number of records scanned.
func GetMessagesFromStreamBestEffort(chatID int64, query MessageStreamQuery) ([]*Message, int, error) {
	return getMessagesFromStream(chatID, query, true)
}

func getMessagesFromStream(chatID int64, query MessageStreamQuery, bestEffort bool) ([]*Message, int, error) {
	key := wrapKeyWithChat("message_stream", chatID)

	records, err := rc.XRevRangeN(context.TODO(), key, "+", "-", messageStreamMaxLen).Result()
	if err != nil {
		log.Error("get messages from redis stream failed", zap.Int64("chat", chatID),
			zap.Int("min", query.MinID), zap.Int("max", query.MaxID), zap.Error(err))
		return nil, 0, err
	}

	// Snapshots are stored concurrently, so an older snapshot may be appended after a newer edit.
	latest := make(map[int]*Message, len(records))
	for _, record := range records {
		seen := false
		if id, ok := streamRecordMessageID(record); ok {
			if !query.contains(id) {
				continue
			}
			_, seen = latest[id]
		}

		message, err := decodeStreamRecord(chatID, record)
		if err != nil {
			if bestEffort || seen {
				continue
			}
			return nil, len(records), err
		}
		if !query.contains(message.ID) {
			continue
		}
		if current, ok := latest[message.ID]; ok && compareMessageSnapshots(message, current) <= 0 {
			continue
		}
		latest[message.ID] = message
	}

	messages := make([]*Message, 0, len(latest))
	for _, message := range latest {
		messages = append(messages, message)
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].ID < messages[j].ID })
	if query.Count > 0 && int64(len(messages)) > query.Count {
		if query.Reverse {
			messages = messages[int64(len(messages))-query.Count:]
		} else {
			messages = messages[:query.Count]
		}
	}
	if query.Reverse {
		slices.Reverse(messages)
	}
	return messages, len(records), nil
}

func (q MessageStreamQuery) contains(id int) bool {
	return (q.MinID <= 0 || id >= q.MinID) && (q.MaxID <= 0 || id <= q.MaxID)
}

func streamRecordMessageID(record redis.XMessage) (int, bool) {
	if raw, ok := record.Values[messageStreamFieldID].(string); ok {
		if id, err := strconv.Atoi(raw); err == nil && id > 0 {
			return id, true
		}
		return 0, false
	}
	seq, _, found := strings.Cut(record.ID, "-")
	if !found {
		return 0, false
	}
	id, err := strconv.Atoi(seq)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func decodeStreamRecord(chatID int64, record redis.XMessage) (*Message, error) {
	encoded, ok := record.Values[messageStreamFieldMessage].(string)
	if !ok {
		err := fmt.Errorf("%w: invalid_stream_message_field", ErrInvalidCachedMessage)
		log.Error("decode cached stream message failed", zap.Int64("chat", chatID), zap.String("stream", record.ID),
			zap.String("field", messageStreamFieldMessage), zap.Error(err))
		return nil, err
	}
	message, err := decodeCachedMessage([]byte(encoded))
	if err != nil {
		log.Error("decode cached stream message failed", zap.Int64("chat", chatID), zap.String("stream", record.ID),
			zap.String("field", messageStreamFieldMessage), zap.Error(err))
		return nil, err
	}
	return message, nil
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
