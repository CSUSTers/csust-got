//go:build !386 && !arm

package chatv2

import (
	"errors"
	"strings"
	"testing"

	"csust-got/chat"
	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

var (
	errTestImageContextBoom    = errors.New("boom")
	errTestImageContextMissing = errors.New("missing")
)

func TestBuildUserMessageBuildsMultimodalImageContext(t *testing.T) {
	oldEncoder := encodeTelegramPhotoDataURL
	encodeTelegramPhotoDataURL = func(_ *TurnContext, photo *tb.Photo) (string, error) {
		return "data:image/jpeg;base64," + photo.FileID, nil
	}
	defer func() { encodeTelegramPhotoDataURL = oldEncoder }()

	tc := &TurnContext{
		Message: &tb.Message{
			ID:    30,
			Photo: &tb.Photo{File: tb.File{FileID: "current"}},
			ReplyTo: &tb.Message{
				ID:    20,
				Photo: &tb.Photo{File: tb.File{FileID: "reply"}},
			},
		},
		Config: &config.ChatConfigSingle{
			Model: &config.Model{
				Features: config.ModelFeatures{Image: true},
			},
			Features: config.FeatureSetting{Image: true},
		},
	}
	history := &RichHistory{
		ContextMessages: []*chat.ContextMessage{{ID: 10, Text: "history"}},
		FullMessages: []*tb.Message{
			{
				ID:      10,
				Photo:   &tb.Photo{File: tb.File{FileID: "history"}},
				Caption: "history caption",
			},
		},
	}

	result := buildUserMessage("请总结一下", tc, history)

	require.Empty(t, result.Content)
	require.Len(t, result.UserInputMultiContent, 4)
	assert.Equal(t, schema.ChatMessagePartTypeText, result.UserInputMultiContent[0].Type)
	assert.Contains(t, result.UserInputMultiContent[0].Text, "Image Context:")
	assert.Contains(t, result.UserInputMultiContent[0].Text, "current message 30")
	assert.Contains(t, result.UserInputMultiContent[0].Text, "reply message 20")
	assert.Contains(t, result.UserInputMultiContent[0].Text, "context message 10")
	assert.Contains(t, result.UserInputMultiContent[0].Text, "history caption")

	require.NotNil(t, result.UserInputMultiContent[1].Image)
	require.NotNil(t, result.UserInputMultiContent[2].Image)
	require.NotNil(t, result.UserInputMultiContent[3].Image)
	assert.Equal(t, "data:image/jpeg;base64,current", *result.UserInputMultiContent[1].Image.URL)
	assert.Equal(t, "data:image/jpeg;base64,reply", *result.UserInputMultiContent[2].Image.URL)
	assert.Equal(t, "data:image/jpeg;base64,history", *result.UserInputMultiContent[3].Image.URL)
}

func TestBuildUserMessageSkipsBrokenImages(t *testing.T) {
	oldEncoder := encodeTelegramPhotoDataURL
	encodeTelegramPhotoDataURL = func(_ *TurnContext, photo *tb.Photo) (string, error) {
		if photo.FileID == "reply" {
			return "", errTestImageContextBoom
		}
		return "data:image/jpeg;base64," + photo.FileID, nil
	}
	defer func() { encodeTelegramPhotoDataURL = oldEncoder }()

	tc := &TurnContext{
		Message: &tb.Message{
			ID:    30,
			Photo: &tb.Photo{File: tb.File{FileID: "current"}},
			ReplyTo: &tb.Message{
				ID:    20,
				Photo: &tb.Photo{File: tb.File{FileID: "reply"}},
			},
		},
		Config: &config.ChatConfigSingle{
			Model: &config.Model{
				Features: config.ModelFeatures{Image: true},
			},
			Features: config.FeatureSetting{Image: true},
		},
	}
	history := &RichHistory{
		FullMessages: []*tb.Message{
			{ID: 10, Photo: &tb.Photo{File: tb.File{FileID: "history"}}},
		},
	}

	result := buildUserMessage("继续", tc, history)

	require.Len(t, result.UserInputMultiContent, 3)
	assert.Contains(t, result.UserInputMultiContent[0].Text, "current message 30")
	assert.NotContains(t, result.UserInputMultiContent[0].Text, "reply message 20")
	assert.Contains(t, result.UserInputMultiContent[0].Text, "context message 10")
	assert.Equal(t, "data:image/jpeg;base64,current", *result.UserInputMultiContent[1].Image.URL)
	assert.Equal(t, "data:image/jpeg;base64,history", *result.UserInputMultiContent[2].Image.URL)
}

func TestBuildUserMessageFallsBackWhenMultimodalDisabled(t *testing.T) {
	tc := &TurnContext{
		Message: &tb.Message{
			ID:    30,
			Photo: &tb.Photo{File: tb.File{FileID: "current"}},
		},
		Config: &config.ChatConfigSingle{
			Model: &config.Model{
				Features: config.ModelFeatures{Image: false},
			},
			Features: config.FeatureSetting{Image: true},
		},
	}

	result := buildUserMessage("hello", tc, nil)

	assert.Empty(t, result.UserInputMultiContent)
	assert.Contains(t, result.Content, "file_id: current")
}

func TestLoadCurrentAlbumMessagesCollectsSiblingMessages(t *testing.T) {
	oldLoader := loadStoredTelegramMessage
	oldWait := currentAlbumCompletionWait
	oldPoll := currentAlbumCompletionPollInterval
	oldWindow := currentAlbumSiblingWindow
	defer func() {
		loadStoredTelegramMessage = oldLoader
		currentAlbumCompletionWait = oldWait
		currentAlbumCompletionPollInterval = oldPoll
		currentAlbumSiblingWindow = oldWindow
	}()

	currentAlbumCompletionWait = 0
	currentAlbumCompletionPollInterval = 0
	currentAlbumSiblingWindow = 2
	loadStoredTelegramMessage = func(chatID int64, messageID int) (*tb.Message, error) {
		switch messageID {
		case 99, 101:
			return &tb.Message{
				ID:      messageID,
				Chat:    &tb.Chat{ID: chatID},
				AlbumID: "album-1",
				Photo:   &tb.Photo{File: tb.File{FileID: "photo"}},
			}, nil
		case 102:
			return &tb.Message{
				ID:      messageID,
				Chat:    &tb.Chat{ID: chatID},
				AlbumID: "album-2",
				Photo:   &tb.Photo{File: tb.File{FileID: "other"}},
			}, nil
		default:
			return nil, errTestImageContextMissing
		}
	}

	result := loadCurrentAlbumMessages(&tb.Message{
		ID:      100,
		Chat:    &tb.Chat{ID: 42},
		AlbumID: "album-1",
		Photo:   &tb.Photo{File: tb.File{FileID: "current"}},
	})

	require.Len(t, result, 3)
	assert.Equal(t, 99, result[0].ID)
	assert.Equal(t, 100, result[1].ID)
	assert.Equal(t, 101, result[2].ID)
}

func TestLoadFullContextMessagesUsesReplyChainAndStoredMessages(t *testing.T) {
	oldLoader := loadStoredTelegramMessage
	defer func() { loadStoredTelegramMessage = oldLoader }()

	loadStoredTelegramMessage = func(chatID int64, messageID int) (*tb.Message, error) {
		if messageID == 10 {
			return &tb.Message{
				ID:    10,
				Chat:  &tb.Chat{ID: chatID},
				Photo: &tb.Photo{File: tb.File{FileID: "stored"}},
			}, nil
		}
		return nil, errTestImageContextMissing
	}

	current := &tb.Message{
		ID:   30,
		Chat: &tb.Chat{ID: 42},
		ReplyTo: &tb.Message{
			ID:    20,
			Photo: &tb.Photo{File: tb.File{FileID: "reply"}},
		},
	}
	contextMsgs := []*chat.ContextMessage{
		{ID: 20, Text: "reply"},
		{ID: 10, Text: "stored"},
	}

	result := loadFullContextMessages(current, contextMsgs)

	require.Len(t, result, 2)
	assert.Same(t, current.ReplyTo, result[0])
	assert.Equal(t, 10, result[1].ID)
}

func TestBuildMessagesUsesRichHistoryContext(t *testing.T) {
	cc := &CompiledChat{}
	tc := &TurnContext{
		Message: &tb.Message{Text: "hello"},
	}
	history := &RichHistory{
		ContextMessages: []*chat.ContextMessage{{ID: 1, User: "alice", Text: "hi"}},
	}

	result, err := BuildMessages(cc, tc, history)

	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, schema.User, result[0].Role)
	assert.Equal(t, schema.User, result[1].Role)
	assert.Equal(t, "hello", result[1].Content)
}

func TestSummarizeImageCaptionTruncatesLongCaptions(t *testing.T) {
	caption := strings.Repeat("a", 90)
	got := summarizeImageCaption(caption)
	assert.Len(t, got, 80)
	assert.True(t, strings.HasSuffix(got, "..."))
}
