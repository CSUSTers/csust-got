package agentv3

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func replySessionSchemaText(messages []*schema.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		if message == nil {
			continue
		}
		builder.WriteString(message.Content)
		for _, part := range message.UserInputMultiContent {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func replySessionTextPart(message *schema.Message) string {
	if message == nil {
		return ""
	}
	if len(message.UserInputMultiContent) > 0 {
		return message.UserInputMultiContent[0].Text
	}
	return message.Content
}

var errReplySessionTestEncodeFailed = errors.New("encode failed")

func replySessionTestContext(cfg *config.AgentConfig, current *tb.Message) *TurnContext {
	return &TurnContext{
		Config:  cfg,
		Message: current,
		ChatID:  -100,
		BotUser: &tb.User{ID: 99, Username: "bot"},
		V3:      &AgentV3TurnState{},
	}
}

func TestReplySessionMessagesPreserveLinksAndCurrentOnce(t *testing.T) {
	cfg := &config.AgentConfig{ContextMode: "reply_chain"}
	old := sessionMessage(1, 7, 0, "earlier")
	current := sessionMessage(2, 7, 40, "site")
	current.Entities = []tb.MessageEntity{{Type: tb.EntityTextLink, Offset: 0, Length: 4, URL: "https://example.invalid/a"}}
	tc := replySessionTestContext(cfg, current)

	got, err := buildReplySessionMessages(&CompiledAgent{Config: cfg}, tc, replySession{
		Blocks: []replySessionBlock{{Messages: []*tb.Message{old, current}, Current: true}},
	}, 24000)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, schema.User, got[0].Role)
	text := replySessionSchemaText(got)
	assert.Contains(t, text, "[site](https://example.invalid/a)")
	assert.Equal(t, 1, strings.Count(text, "[site]("))
	assert.Less(t, strings.Index(text, "earlier"), strings.Index(text, "[site]("))
	assert.Equal(t, "site", current.Text)
}

func TestReplySessionMessagesHardBudgetKeepsLatestUTF8AndAttachments(t *testing.T) {
	cfg := &config.AgentConfig{ContextMode: "reply_chain"}
	old := sessionMessage(1, 7, 0, strings.Repeat("old", 1000))
	current := sessionMessage(2, 7, 40, strings.Repeat("早", 1000)+"LATEST_REQUEST")
	current.Document = &tb.Document{File: tb.File{FileID: "document-file-id"}, FileName: "attachment.pdf", MIME: "application/pdf"}
	tc := replySessionTestContext(cfg, current)

	got, err := buildReplySessionMessages(&CompiledAgent{Config: cfg}, tc, replySession{
		Blocks: []replySessionBlock{{Messages: []*tb.Message{old, current}, Current: true}},
	}, 450)
	require.NoError(t, err)
	text := replySessionSchemaText(got)
	assert.LessOrEqual(t, len(text), 450)
	assert.True(t, utf8.ValidString(text))
	assert.Contains(t, text, "LATEST_REQUEST")
	assert.Contains(t, text, "[earlier content omitted]")
	assert.Contains(t, text, "document-file-id")
	assert.NotContains(t, text, "oldold")
	assert.Equal(t, strings.Repeat("早", 1000)+"LATEST_REQUEST", current.Text)
}

func TestReplySessionMessagesHardBudgetMarksSingleTruncatedTrigger(t *testing.T) {
	cfg := &config.AgentConfig{ContextMode: "reply_chain"}
	current := sessionMessage(2, 7, 40, strings.Repeat("早", 1000)+"LATEST_REQUEST")
	tc := replySessionTestContext(cfg, current)

	got, err := buildReplySessionMessages(&CompiledAgent{Config: cfg}, tc, replySession{
		Blocks: []replySessionBlock{{Messages: []*tb.Message{current}, Current: true}},
	}, 300)
	require.NoError(t, err)
	text := replySessionSchemaText(got)
	assert.LessOrEqual(t, len(text), 300)
	assert.True(t, utf8.ValidString(text))
	assert.Contains(t, text, "LATEST_REQUEST")
	assert.Contains(t, text, "[earlier content omitted]")
	assert.Equal(t, strings.Repeat("早", 1000)+"LATEST_REQUEST", current.Text)
}

func TestReplySessionMessagesHardBudgetKeepsFormattedSuffix(t *testing.T) {
	const maxRawTokens = 512
	text := strings.Repeat("早", 1000) + "LATEST_REQUEST"
	current := sessionMessage(2, 7, 40, text)
	current.Entities = []tb.MessageEntity{{
		Type:   tb.EntityBold,
		Offset: 0,
		Length: replySessionUTF16Length(text),
	}}
	originalEntities := append(tb.Entities(nil), current.Entities...)
	tc := replySessionTestContext(&config.AgentConfig{ContextMode: "reply_chain"}, current)

	got, err := buildReplySessionMessages(&CompiledAgent{}, tc, replySession{
		Blocks: []replySessionBlock{{Messages: []*tb.Message{current}, Current: true}},
	}, approxAgentV3TokenCharLimit(maxRawTokens))
	require.NoError(t, err)
	textParts := replySessionSchemaText(got)
	assert.LessOrEqual(t, len(textParts), approxAgentV3TokenCharLimit(maxRawTokens))
	assert.True(t, utf8.ValidString(textParts))
	assert.Contains(t, textParts, "LATEST_REQUEST")
	assert.Equal(t, text, current.Text)
	assert.Equal(t, originalEntities, current.Entities)
}

func TestReplySessionSuffixKeepsFormattingAndDropsPartialLinks(t *testing.T) {
	prefix := strings.Repeat("早", 1000) + "😀"
	const linkText = "LINK"
	const tail = "LATEST_REQUEST"
	text := prefix + linkText + tail
	message := sessionMessage(2, 7, 40, text)
	message.Entities = []tb.MessageEntity{
		{Type: tb.EntityBold, Offset: 0, Length: replySessionUTF16Length(text)},
		{Type: tb.EntityTextLink, Offset: replySessionUTF16Length(prefix), Length: replySessionUTF16Length(linkText), URL: "https://example.invalid/link"},
	}
	originalEntities := append(tb.Entities(nil), message.Entities...)

	trimmed := replySessionMessageWithSuffix(message, len("INK"+tail))
	require.Equal(t, tail, trimmed.Text)
	require.Len(t, trimmed.Entities, 1)
	assert.Equal(t, tb.EntityBold, trimmed.Entities[0].Type)
	assert.Equal(t, 0, trimmed.Entities[0].Offset)
	assert.Equal(t, replySessionUTF16Length(tail), trimmed.Entities[0].Length)
	assertReplySessionEntityBounds(t, trimmed)
	assert.True(t, utf8.ValidString(getMessageTextWithEntities(trimmed, false)))
	assert.NotContains(t, getMessageTextWithEntities(trimmed, false), "example.invalid")
	assert.Equal(t, text, message.Text)
	assert.Equal(t, originalEntities, message.Entities)
}

func TestReplySessionMessagesImageManifestKeepsCaptionUTF8(t *testing.T) {
	oldEncoder := encodeTelegramPhotoDataURL
	encodeTelegramPhotoDataURL = func(*TurnContext, *tb.Photo) (string, error) {
		return "data:image/jpeg;base64,aA==", nil
	}
	t.Cleanup(func() { encodeTelegramPhotoDataURL = oldEncoder })

	caption := strings.Repeat("早", 30)
	current := sessionMessage(2, 7, 40, "")
	current.Caption = caption
	current.Photo = &tb.Photo{File: tb.File{FileID: "caption-photo"}}
	cfg := &config.AgentConfig{
		ContextMode: "reply_chain",
		Model:       &config.Model{Features: config.ModelFeatures{Image: true}},
		Features:    config.FeatureSetting{Image: true},
	}
	got, err := buildReplySessionMessages(&CompiledAgent{Config: cfg}, replySessionTestContext(cfg, current), replySession{
		Blocks: []replySessionBlock{{Messages: []*tb.Message{current}, Current: true}},
	}, 24000)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Len(t, got[0].UserInputMultiContent, 2)
	require.NotNil(t, got[0].UserInputMultiContent[1].Image)
	for _, part := range got[0].UserInputMultiContent {
		assert.True(t, utf8.ValidString(part.Text))
	}
	assert.Equal(t, 1, strings.Count(got[0].UserInputMultiContent[0].Text, caption))
	assert.Contains(t, got[0].UserInputMultiContent[0].Text, "caption-photo")
}

func assertReplySessionEntityBounds(t *testing.T, message *tb.Message) {
	t.Helper()
	text := message.Text
	entities := message.Entities
	if text == "" {
		text, entities = message.Caption, message.CaptionEntities
	}
	limit := replySessionUTF16Length(text)
	for _, entity := range entities {
		assert.GreaterOrEqual(t, entity.Offset, 0)
		assert.Greater(t, entity.Length, 0)
		assert.LessOrEqual(t, entity.Offset+entity.Length, limit)
	}
}

func TestReplySessionMessagesKeepImagesInTheirBlocks(t *testing.T) {
	oldEncoder := encodeTelegramPhotoDataURL
	oldLoader := loadStoredTelegramMessage
	encoded := make([]string, 0)
	encodeTelegramPhotoDataURL = func(_ *TurnContext, photo *tb.Photo) (string, error) {
		encoded = append(encoded, photo.FileID)
		return "data:image/jpeg;base64,aA==", nil
	}
	loadStoredTelegramMessage = func(int64, int) (*tb.Message, error) {
		t.Fatal("reply-session rendering must not load album siblings")
		return nil, nil
	}
	t.Cleanup(func() {
		encodeTelegramPhotoDataURL = oldEncoder
		loadStoredTelegramMessage = oldLoader
	})

	cfg := &config.AgentConfig{
		ContextMode: "reply_chain",
		Model:       &config.Model{Features: config.ModelFeatures{Image: true, ImageBase64Raw: true}},
		Features:    config.FeatureSetting{Image: true},
	}
	history := sessionMessage(1, 7, 0, "history image")
	history.Photo = &tb.Photo{File: tb.File{FileID: "history-file"}}
	bot := sessionMessage(2, 99, 1, "bot image")
	bot.Sender.IsBot = true
	bot.Photo = &tb.Photo{File: tb.File{FileID: "bot-file"}}
	current := sessionMessage(3, 7, 2, "current image")
	current.Photo = &tb.Photo{File: tb.File{FileID: "current-file"}}
	current.AlbumID = "album"
	current.ReplyTo = &tb.Message{ID: 2, Photo: &tb.Photo{File: tb.File{FileID: "reply-file"}}}
	current.Document = &tb.Document{File: tb.File{FileID: "document-file"}, FileName: "notes.pdf", MIME: "application/pdf"}
	current.Sticker = &tb.Sticker{File: tb.File{FileID: "sticker-file"}, Emoji: "🙂"}
	tc := replySessionTestContext(cfg, current)

	got, err := buildReplySessionMessages(&CompiledAgent{Config: cfg}, tc, replySession{Blocks: []replySessionBlock{
		{Messages: []*tb.Message{history}},
		{Messages: []*tb.Message{bot}},
		{Messages: []*tb.Message{current}, Current: true},
	}}, 24000)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, schema.User, got[0].Role)
	assert.Equal(t, schema.Assistant, got[1].Role)
	assert.Equal(t, schema.User, got[2].Role)
	assert.Contains(t, replySessionTextPart(got[0]), "history-file")
	assert.NotContains(t, replySessionTextPart(got[0]), "current-file")
	assert.Contains(t, replySessionTextPart(got[1]), "bot-file")
	assert.Empty(t, got[1].UserInputMultiContent)
	assert.Contains(t, replySessionTextPart(got[2]), "current-file")
	assert.Contains(t, replySessionTextPart(got[2]), "document-file")
	assert.Contains(t, replySessionTextPart(got[2]), "sticker-file")
	assert.NotContains(t, replySessionTextPart(got[2]), "reply-file")
	require.Len(t, got[0].UserInputMultiContent, 2)
	require.Len(t, got[2].UserInputMultiContent, 2)
	assert.Equal(t, "aA==", *got[0].UserInputMultiContent[1].Image.URL)
	assert.Equal(t, "aA==", *got[2].UserInputMultiContent[1].Image.URL)
	assert.Equal(t, []string{"history-file", "current-file"}, encoded)
	assert.ElementsMatch(t, []orm.AgentV3ImageRef{
		{MessageID: 1, FileID: "history-file"},
		{MessageID: 2, FileID: "bot-file"},
		{MessageID: 3, FileID: "current-file"},
	}, tc.V3.ImageRefs)
}

func TestReplySessionMessagesRespectImageFeatureGatesAndEncodingFailures(t *testing.T) {
	t.Run("feature disabled", func(t *testing.T) {
		oldEncoder := encodeTelegramPhotoDataURL
		encodeTelegramPhotoDataURL = func(*TurnContext, *tb.Photo) (string, error) {
			t.Fatal("disabled multimodal input must not encode a photo")
			return "", nil
		}
		t.Cleanup(func() { encodeTelegramPhotoDataURL = oldEncoder })

		cfg := &config.AgentConfig{ContextMode: "reply_chain", Features: config.FeatureSetting{Image: true}}
		current := sessionMessage(1, 7, 0, "request")
		current.Photo = &tb.Photo{File: tb.File{FileID: "photo-file"}}
		tc := replySessionTestContext(cfg, current)
		got, err := buildReplySessionMessages(&CompiledAgent{Config: cfg}, tc, replySession{Blocks: []replySessionBlock{{Messages: []*tb.Message{current}, Current: true}}}, 24000)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Empty(t, got[0].UserInputMultiContent)
		assert.Contains(t, got[0].Content, "photo-file")
		assert.Equal(t, []orm.AgentV3ImageRef{{MessageID: 1, FileID: "photo-file"}}, tc.V3.ImageRefs)
	})

	t.Run("encoding failure keeps reference", func(t *testing.T) {
		oldEncoder := encodeTelegramPhotoDataURL
		encodeTelegramPhotoDataURL = func(*TurnContext, *tb.Photo) (string, error) {
			return "", errReplySessionTestEncodeFailed
		}
		t.Cleanup(func() { encodeTelegramPhotoDataURL = oldEncoder })

		cfg := &config.AgentConfig{
			ContextMode: "reply_chain",
			Model:       &config.Model{Features: config.ModelFeatures{Image: true}},
			Features:    config.FeatureSetting{Image: true},
		}
		current := sessionMessage(1, 7, 0, "request")
		current.Photo = &tb.Photo{File: tb.File{FileID: "broken-photo"}}
		tc := replySessionTestContext(cfg, current)
		got, err := buildReplySessionMessages(&CompiledAgent{Config: cfg}, tc, replySession{Blocks: []replySessionBlock{{Messages: []*tb.Message{current}, Current: true}}}, 24000)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Len(t, got[0].UserInputMultiContent, 1)
		assert.Contains(t, replySessionTextPart(got[0]), "broken-photo")
		assert.Equal(t, []orm.AgentV3ImageRef{{MessageID: 1, FileID: "broken-photo"}}, tc.V3.ImageRefs)
	})
}
