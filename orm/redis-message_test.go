package orm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"csust-got/log"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	. "gopkg.in/telebot.v3"
)

func TestGetMessagePollTypeCacheCompatibility(t *testing.T) {
	for _, pollType := range []PollType{PollRegular, PollQuiz} {
		t.Run("marshaled_object_"+string(pollType), func(t *testing.T) {
			setupMessageCacheRedis(t)
			message := cachedPollMessage(1, pollType)
			encoded, err := json.Marshal(message)
			require.NoError(t, err)
			require.Contains(t, string(encoded), fmt.Sprintf(`"type":{"type":%q}`, pollType))
			require.NoError(t, SetMessage(message))

			got, err := GetMessage(message.Chat.ID, message.ID)
			require.NoError(t, err)
			require.Equal(t, pollType, got.Poll.Type)
		})

		t.Run("telegram_string_"+string(pollType), func(t *testing.T) {
			miniRedis := setupMessageCacheRedis(t)
			message := cachedPollMessage(2, pollType)
			encoded := cachedMessageJSON(t, message)
			objectType := fmt.Sprintf(`"type":{"type":%q}`, pollType)
			stringType := fmt.Sprintf(`"type":%q`, pollType)
			encoded = []byte(strings.ReplaceAll(string(encoded), objectType, stringType))
			miniRedis.Set(wrapKeyWithChatMsg("message_full", message.Chat.ID, message.ID), string(encoded))

			got, err := GetMessage(message.Chat.ID, message.ID)
			require.NoError(t, err)
			require.Equal(t, pollType, got.Poll.Type)
		})
	}
}

func TestGetMessageRepairsNestedPollTypesWithoutLosingFields(t *testing.T) {
	setupMessageCacheRedis(t)
	message := cachedPollMessage(10, PollRegular)
	message.Photo = &Photo{File: File{FileID: "photo-id", UniqueID: "photo-unique", FileSize: 9007199254740993}, Width: 1280, Height: 720}
	message.ReplyTo = cachedPollMessage(9, PollQuiz)
	message.PinnedMessage = cachedPollMessage(8, PollRegular)
	message.ExternalReplyInfo = &ExternalReplyInfo{
		Poll: cachedPollMessage(7, PollQuiz).Poll,
		Chat: &Chat{ID: -200, PinnedMessage: cachedPollMessage(6, PollRegular)},
	}
	message.Chat.PinnedMessage = cachedPollMessage(5, PollQuiz)

	require.NoError(t, SetMessage(message))
	got, err := GetMessage(message.Chat.ID, message.ID)
	require.NoError(t, err)
	require.Equal(t, PollRegular, got.Poll.Type)
	require.Equal(t, PollQuiz, got.ReplyTo.Poll.Type)
	require.Equal(t, PollRegular, got.PinnedMessage.Poll.Type)
	require.Equal(t, PollQuiz, got.ExternalReplyInfo.Poll.Type)
	require.Equal(t, PollRegular, got.ExternalReplyInfo.Chat.PinnedMessage.Poll.Type)
	require.Equal(t, PollQuiz, got.Chat.PinnedMessage.Poll.Type)
	require.Equal(t, int64(9007199254740993), got.Sender.ID)
	require.Equal(t, int64(9007199254740993), got.Photo.FileSize)
	require.Equal(t, "photo-id", got.Photo.FileID)
	require.Equal(t, 1280, got.Photo.Width)
}

func TestGetMessageRejectsInvalidCachedJSON(t *testing.T) {
	tests := map[string]string{
		"unknown_object": `{"message_id":1,"chat":{"id":-100},"poll":{"type":{"type":"other"}}}`,
		"object_shape":   `{"message_id":1,"chat":{"id":-100},"poll":{"type":{"type":"regular","extra":true}}}`,
		"unknown_string": `{"message_id":1,"chat":{"id":-100},"poll":{"type":"other"}}`,
		"invalid_scalar": `{"message_id":1,"chat":{"id":-100},"poll":{"type":42}}`,
		"truncated":      `{"message_id":1,"chat":{"id":-100},`,
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			miniRedis := setupMessageCacheRedis(t)
			miniRedis.Set(wrapKeyWithChatMsg("message_full", -100, 1), encoded)

			_, err := GetMessage(-100, 1)
			require.ErrorIs(t, err, ErrInvalidCachedMessage)
		})
	}
}

func TestMessageStreamStrictAndBestEffortDecoding(t *testing.T) {
	t.Run("strict_decodes_marshaled_poll", func(t *testing.T) {
		setupMessageCacheRedis(t)
		require.NoError(t, PushMessageToStream(cachedPollMessage(1, PollQuiz)))

		messages, err := GetMessagesFromStream(-100, "-", "+", 10, false)
		require.NoError(t, err)
		require.Len(t, messages, 1)
		require.Equal(t, PollQuiz, messages[0].Poll.Type)
	})

	t.Run("strict_reports_bad_record_without_panicking", func(t *testing.T) {
		setupMessageCacheRedis(t)
		addRawMessageStreamRecord(t, -100, "1-0", map[string]any{"other": "field"})

		var err error
		require.NotPanics(t, func() {
			_, err = GetMessagesFromStream(-100, "-", "+", 10, false)
		})
		require.ErrorIs(t, err, ErrInvalidCachedMessage)
	})

	t.Run("best_effort_skips_bad_records_and_reports_scanned_count", func(t *testing.T) {
		setupMessageCacheRedis(t)
		addRawMessageStreamRecord(t, -100, "1-0", map[string]any{"message": `{"message_id":1`})
		require.NoError(t, PushMessageToStream(cachedPollMessage(2, PollRegular)))

		messages, scanned, err := GetMessagesFromStreamBestEffort(-100, "-", "+", 10, false)
		require.NoError(t, err)
		require.Equal(t, 2, scanned)
		require.Len(t, messages, 1)
		require.Equal(t, 2, messages[0].ID)
	})
}

func TestMessageStreamRedisErrorsRemainDistinct(t *testing.T) {
	miniRedis := setupMessageCacheRedis(t)
	miniRedis.Close()

	_, _, err := GetMessagesFromStreamBestEffort(-100, "-", "+", 10, false)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrInvalidCachedMessage))
}

func cachedPollMessage(id int, pollType PollType) *Message {
	return &Message{
		ID:     id,
		Chat:   &Chat{ID: -100},
		Sender: &User{ID: 9007199254740993},
		Poll:   &Poll{ID: "poll", Type: pollType, Question: "question"},
	}
}

func setupMessageCacheRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	miniRedis := setupAgentV3Redis(t)
	log.InitLogger()
	return miniRedis
}

func cachedMessageJSON(t *testing.T, message *Message) []byte {
	t.Helper()
	encoded, err := json.Marshal(message)
	require.NoError(t, err)
	return encoded
}

func addRawMessageStreamRecord(t *testing.T, chatID int64, id string, values map[string]any) {
	t.Helper()
	err := rc.XAdd(t.Context(), &redis.XAddArgs{
		Stream: wrapKeyWithChat("message_stream", chatID),
		ID:     id,
		Values: values,
	}).Err()
	require.NoError(t, err)
}
